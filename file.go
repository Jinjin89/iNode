package inode

import (
	"os"
	"path/filepath"
)

func writeFile(path string, data []byte, mode os.FileMode, durable bool) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".inode-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = file.Chmod(mode)
	if err == nil {
		_, err = file.Write(data)
	}
	if err == nil && durable {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err == nil && durable {
		err = syncDirectory(filepath.Dir(path))
	}
	return err
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	if closeErr := directory.Close(); err == nil {
		err = closeErr
	}
	return err
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
