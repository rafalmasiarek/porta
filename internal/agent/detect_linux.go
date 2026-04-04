//go:build linux

package agent

import "context"

func detectVolumes() ([]MountedVolume, error) {
    return []MountedVolume{}, nil
}

func watchVolumeChanges(ctx context.Context, trigger chan<- struct{}) error {
    <-ctx.Done()
    return nil
}