package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/containerd/cgroups"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/typeurl"
	"github.com/crosbymichael/boss/api/v1"
	"github.com/crosbymichael/boss/config"
	"github.com/crosbymichael/boss/system"
	units "github.com/docker/go-units"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

var listCommand = cli.Command{
	Name:  "list",
	Usage: "list containers managed via boss",
	Action: func(clix *cli.Context) error {
		ctx := system.Context()
		client, err := system.NewClient()
		if err != nil {
			return err
		}
		defer client.Close()
		containers, err := client.Containers(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 10, 1, 3, ' ', 0)
		const tfmt = "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n"
		fmt.Fprint(w, "ID\tIMAGE\tSTATUS\tIP\tCPU\tMEMORY\tPIDS\tSIZE\n")
		for _, c := range containers {
			info, err := c.Info(ctx)
			if err != nil {
				return err
			}
			task, err := c.Task(ctx, nil)
			if err != nil {
				if errdefs.IsNotFound(err) {
					fmt.Fprintf(w, tfmt, c.ID(), info.Image, containerd.Stopped, "", "0s", "0/0", "0/0", "0")
					continue
				}
				logrus.WithError(err).Errorf("load task %s", c.ID())
				continue
			}
			status, err := task.Status(ctx)
			if err != nil {
				return err
			}
			stats, err := task.Metrics(ctx)
			if err != nil {
				return err
			}
			d := info.Extensions[config.CurrentConfig]
			cfg, err := config.UnmarshalConfig(&d)
			if err != nil {
				return err
			}
			v, err := typeurl.UnmarshalAny(stats.Data)
			if err != nil {
				return err
			}
			var (
				cg      = v.(*cgroups.Metrics)
				cpu     = time.Duration(int64(cg.CPU.Usage.Total))
				memory  = units.HumanSize(float64(cg.Memory.Usage.Usage - cg.Memory.TotalCache))
				limit   = units.HumanSize(float64(cg.Memory.Usage.Limit))
				service = client.SnapshotService(info.Snapshotter)
			)
			usage, err := service.Usage(ctx, info.SnapshotKey)
			if err != nil {
				return err
			}
			bindSizes, err := getBindSizes(cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, tfmt,
				c.ID(),
				info.Image,
				status.Status,
				info.Labels[v1.IPLabel],
				cpu,
				fmt.Sprintf("%s/%s", memory, limit),
				fmt.Sprintf("%d/%d", cg.Pids.Current, cg.Pids.Limit),
				units.HumanSize(float64(usage.Size+bindSizes)),
			)
		}
		return w.Flush()
	},
}

func getBindSizes(c *v1.Container) (size int64, _ error) {
	for _, m := range c.Mounts {
		f, err := os.Open(m.Source)
		if err != nil {
			return size, err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return size, err
		}
		if info.IsDir() {
			f.Close()
			if err := filepath.Walk(m.Source, func(path string, wi os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if wi.IsDir() {
					return nil
				}
				size += wi.Size()
				return nil
			}); err != nil {
				return size, err
			}
			continue
		}
		size += info.Size()
		f.Close()
	}
	return size, nil
}
