package adapter

import (
	"net"
	"syscall"
)

// peerPID is the pid of the process on the other end of a unix socket
// (LOCAL_PEERPID, at level SOL_LOCAL = 0).
func peerPID(c *net.UnixConn) (int, error) {
	const solLocal, localPeerPID = 0, 2
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var serr error
	if err := raw.Control(func(fd uintptr) { pid, serr = syscall.GetsockoptInt(int(fd), solLocal, localPeerPID) }); err != nil {
		return 0, err
	}
	return pid, serr
}
