package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/store"
)

// backupFiles are the files of the data directory besides the database that a
// restore needs. Without master.key, secrets cannot be decrypted.
var backupFiles = []string{"master.key", "egress/ca.crt", "egress/ca.key"}

// runBackup writes a .tar.gz with a consistent database snapshot, the master
// key and the egress CA. It works while the server is running.
func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	out := fs.String("out", "", "output file (.tar.gz); default <data_dir>/backups/agenthold-<time>.tar.gz")
	_ = fs.Parse(args)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *out == "" {
		*out = filepath.Join(cfg.DataDir, "backups", "agenthold-"+time.Now().UTC().Format("20060102-150405")+".tar.gz")
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o700); err != nil {
		return err
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, "agenthold.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	tmp, err := os.MkdirTemp(filepath.Dir(*out), ".snapshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, "agenthold.db")
	if err := st.Snapshot(ctx, snap); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	add := func(src, name string) error {
		fi, err := os.Stat(src)
		if err != nil {
			return err
		}
		h, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		h.Name = name
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		r, err := os.Open(src)
		if err != nil {
			return err
		}
		defer r.Close()
		_, err = io.Copy(tw, r)
		return err
	}
	if err := add(snap, "agenthold.db"); err != nil {
		return err
	}
	for _, name := range backupFiles {
		if err := add(filepath.Join(cfg.DataDir, name), name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Println(*out)
	return nil
}

// runRestore unpacks a backup into the data directory. The server must be
// stopped. Existing files are moved aside to <name>.before-restore.
func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: agenthold restore <backup.tar.gz>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	allowed := map[string]bool{"agenthold.db": true}
	for _, n := range backupFiles {
		allowed[n] = true
	}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if !allowed[h.Name] {
			return fmt.Errorf("unexpected file in backup: %s", h.Name)
		}
		dst := filepath.Join(cfg.DataDir, h.Name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if _, err := os.Stat(dst); err == nil {
			if err := os.Rename(dst, dst+".before-restore"); err != nil {
				return err
			}
		}
		if h.Name == "agenthold.db" {
			for _, ext := range []string{"-wal", "-shm"} {
				_ = os.Rename(dst+ext, dst+ext+".before-restore")
			}
		}
		w, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, tr); err != nil {
			_ = w.Close()
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		fmt.Println("restored", h.Name)
	}
	return nil
}
