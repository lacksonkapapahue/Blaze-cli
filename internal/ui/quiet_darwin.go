package ui

import "golang.org/x/sys/unix"

const ioctlGet, ioctlSet = unix.TIOCGETA, unix.TIOCSETA
