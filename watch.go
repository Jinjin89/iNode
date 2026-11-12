package inode

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

type watch func(func(Values) error) error

var folderInterval = time.Second

func scheduled(ctx context.Context, next func(time.Time) time.Time) watch {
	return func(submit func(Values) error) error {
		at := next(time.Now())
		timer := time.NewTimer(time.Until(at))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				if err := submit(Values{"time": at.Format(time.RFC3339Nano)}); err != nil {
					return err
				}
				at = next(at)
				timer.Reset(time.Until(at))
			}
		}
	}
}

func every(ctx context.Context, in Values) (watch, error) {
	interval := time.Duration(in["interval"].(float64) * float64(time.Second))
	if interval <= 0 {
		return nil, Problem{Input: "interval", Message: "use a positive interval in seconds, at least one nanosecond"}
	}
	return scheduled(ctx, func(at time.Time) time.Time { return at.Add(interval) }), nil
}

func daily(ctx context.Context, in Values) (watch, error) {
	at, err := time.Parse("15:04", in["at"].(string))
	if err != nil {
		return nil, Problem{Input: "at", Message: "use a local time as HH:MM, for example 08:00"}
	}
	return scheduled(ctx, func(now time.Time) time.Time {
		for day := now.Day(); ; day++ {
			next := time.Date(now.Year(), now.Month(), day, at.Hour(), at.Minute(), 0, 0, now.Location())
			if next.After(now) && next.Hour() == at.Hour() && next.Minute() == at.Minute() {
				return next
			}
		}
	}), nil
}

func newFile(ctx context.Context, in Values) (watch, error) {
	path := in["path"].(string)
	known, err := fileNames(path)
	if err != nil {
		return nil, err
	}
	interval := folderInterval
	return func(submit func(Values) error) error {
		timer := time.NewTicker(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
			names, err := fileNames(path)
			if err != nil {
				return err
			}
			for _, name := range keys(names) {
				if !known[name] {
					if err := submit(Values{"path": filepath.Join(path, name), "name": name}); err != nil {
						return err
					}
				}
			}
			known = names
		}
	}, nil
}

func fileNames(path string) (map[string]bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			names[entry.Name()] = true
		}
	}
	return names, nil
}
