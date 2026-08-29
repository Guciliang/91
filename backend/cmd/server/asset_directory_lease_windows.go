//go:build windows

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Keep the lock range away from the metadata at the start of the file. Windows
// byte-range locks also block reads through other handles, so locking byte 0
// prevents a competing process from reporting the current owner details.
const assetLockByteOffset uint64 = 1 << 63

func assetLockOverlapped() windows.Overlapped {
	return windows.Overlapped{
		Offset:     uint32(assetLockByteOffset & 0xffffffff),
		OffsetHigh: uint32(assetLockByteOffset >> 32),
	}
}

func lockAssetFile(file *os.File) error {
	overlapped := assetLockOverlapped()
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
}

func unlockAssetFile(file *os.File) error {
	overlapped := assetLockOverlapped()
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}

func isAssetLockBusy(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
