package daemon

import (
	"errors"
	"fmt"
	"os"
)

type restartImage struct {
	executable string
	args       []string
	env        []string
}

func captureRestartImage() (restartImage, error) {
	exe, err := os.Executable()
	if err != nil {
		return restartImage{}, fmt.Errorf("resolve daemon executable: %w", err)
	}
	return restartImage{executable: exe, args: append([]string(nil), os.Args...), env: os.Environ()}, nil
}

func RunRestartable() error {
	image, imageErr := captureRestartImage()
	err := Run()
	if !errors.Is(err, ErrRestartForUpdate) {
		return err
	}
	if imageErr != nil {
		return fmt.Errorf("restart for update: %w", imageErr)
	}
	return execRestartImage(image)
}
