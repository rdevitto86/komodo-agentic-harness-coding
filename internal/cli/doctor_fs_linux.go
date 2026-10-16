package cli

import (
	"fmt"
	"syscall"
)

// fsMagic names the file systems doctor recognises by the magic number statfs returns.
var fsMagic = map[int64]string{
	0x6969:     "nfs",
	0x517B:     "smb",
	0xFF534D42: "cifs",
	0xFE534D42: "smb2",
	0x5346414F: "afs",
	0x01021997: "9p",
	0x00C36400: "ceph",
	0xEF53:     "ext4",
	0x9123683E: "btrfs",
	0x58465342: "xfs",
	0x01021994: "tmpfs",
}

// fsTypeOf returns the name of the file system under dir, or its magic number when unknown.
func fsTypeOf(dir string) (string, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return "", err
	}
	if n, ok := fsMagic[int64(st.Type)]; ok {
		return n, nil
	}
	return fmt.Sprintf("0x%x", int64(st.Type)), nil
}
