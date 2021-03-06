package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/restic/restic"
	"github.com/restic/restic/backend"
	"golang.org/x/crypto/ssh/terminal"
)

type CmdBackup struct{}

func init() {
	_, err := parser.AddCommand("backup",
		"save file/directory",
		"The backup command creates a snapshot of a file or directory",
		&CmdBackup{})
	if err != nil {
		panic(err)
	}
}

func format_bytes(c uint64) string {
	b := float64(c)

	switch {
	case c > 1<<40:
		return fmt.Sprintf("%.3f TiB", b/(1<<40))
	case c > 1<<30:
		return fmt.Sprintf("%.3f GiB", b/(1<<30))
	case c > 1<<20:
		return fmt.Sprintf("%.3f MiB", b/(1<<20))
	case c > 1<<10:
		return fmt.Sprintf("%.3f KiB", b/(1<<10))
	default:
		return fmt.Sprintf("%dB", c)
	}
}

func format_seconds(sec uint64) string {
	hours := sec / 3600
	sec -= hours * 3600
	min := sec / 60
	sec -= min * 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, min, sec)
	}

	return fmt.Sprintf("%d:%02d", min, sec)
}

func format_duration(d time.Duration) string {
	sec := uint64(d / time.Second)
	return format_seconds(sec)
}

func print_tree2(indent int, t *restic.Tree) {
	for _, node := range t.Nodes {
		if node.Tree() != nil {
			fmt.Printf("%s%s/\n", strings.Repeat("  ", indent), node.Name)
			print_tree2(indent+1, node.Tree())
		} else {
			fmt.Printf("%s%s\n", strings.Repeat("  ", indent), node.Name)
		}
	}
}

func (cmd CmdBackup) Usage() string {
	return "DIR/FILE [snapshot-ID]"
}

func (cmd CmdBackup) Execute(args []string) error {
	if len(args) == 0 || len(args) > 2 {
		return fmt.Errorf("wrong number of parameters, Usage: %s", cmd.Usage())
	}

	s, err := OpenRepo()
	if err != nil {
		return err
	}

	var parentSnapshotID backend.ID

	target := args[0]
	if len(args) > 1 {
		parentSnapshotID, err = s.FindSnapshot(args[1])
		if err != nil {
			return fmt.Errorf("invalid id %q: %v", args[1], err)
		}

		fmt.Printf("found parent snapshot %v\n", parentSnapshotID)
	}

	fmt.Printf("scan %s\n", target)

	scanProgress := restic.NewProgress(time.Second)
	if terminal.IsTerminal(int(os.Stdout.Fd())) {
		scanProgress.F = func(s restic.Stat, d time.Duration, ticker bool) {
			fmt.Printf("\x1b[2K\r[%s] %d directories, %d files, %s", format_duration(d), s.Dirs, s.Files, format_bytes(s.Bytes))
		}
		scanProgress.D = func(s restic.Stat, d time.Duration, ticker bool) {
			fmt.Printf("\nDone in %s\n", format_duration(d))
		}
	}

	// TODO: add filter
	// arch.Filter = func(dir string, fi os.FileInfo) bool {
	// 	return true
	// }

	sc := restic.NewScanner(scanProgress)

	newTree, err := sc.Scan(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return err
	}

	if parentSnapshotID != nil {
		fmt.Printf("load old snapshot\n")
		sn, err := restic.LoadSnapshot(s, parentSnapshotID)
		if err != nil {
			return err
		}

		oldTree, err := restic.LoadTreeRecursive(filepath.Dir(sn.Dir), s, sn.Tree)
		if err != nil {
			return err
		}

		err = newTree.CopyFrom(oldTree, &s)
		if err != nil {
			return err
		}
	}

	archiveProgress := restic.NewProgress(time.Second)
	targetStat := newTree.StatTodo()

	if terminal.IsTerminal(int(os.Stdout.Fd())) {
		var bps, eta uint64
		itemsTodo := targetStat.Files + targetStat.Dirs

		archiveProgress.F = func(s restic.Stat, d time.Duration, ticker bool) {
			sec := uint64(d / time.Second)
			if targetStat.Bytes > 0 && sec > 0 && ticker {
				bps = s.Bytes / sec
				if bps > 0 {
					eta = (targetStat.Bytes - s.Bytes) / bps
				}
			}

			itemsDone := s.Files + s.Dirs
			fmt.Printf("\x1b[2K\r[%s] %3.2f%%  %s/s  %s / %s  %d / %d items  ETA %s",
				format_duration(d),
				float64(s.Bytes)/float64(targetStat.Bytes)*100,
				format_bytes(bps),
				format_bytes(s.Bytes), format_bytes(targetStat.Bytes),
				itemsDone, itemsTodo,
				format_seconds(eta))
		}

		archiveProgress.D = func(s restic.Stat, d time.Duration, ticker bool) {
			sec := uint64(d / time.Second)
			fmt.Printf("\nduration: %s, %.2fMiB/s\n",
				format_duration(d),
				float64(targetStat.Bytes)/float64(sec)/(1<<20))
		}
	}

	arch, err := restic.NewArchiver(s, archiveProgress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "err: %v\n", err)
	}

	arch.Error = func(dir string, fi os.FileInfo, err error) error {
		// TODO: make ignoring errors configurable
		fmt.Fprintf(os.Stderr, "\x1b[2K\rerror for %s: %v\n", dir, err)
		return nil
	}

	_, id, err := arch.Snapshot(target, newTree, parentSnapshotID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}

	plen, err := s.PrefixLength(backend.Snapshot)
	if err != nil {
		return err
	}

	fmt.Printf("snapshot %s saved\n", id[:plen])

	return nil
}
