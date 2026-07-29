package local

type recoveredProcessHandle interface {
	Running() (bool, error)
	Terminate() error
	Kill() error
	Close() error
}

type recoveredProcessOpener func(pid int, expectedIdentity string) (recoveredProcessHandle, error)
