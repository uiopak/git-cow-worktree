package main

// fileStat contains Git's 32-bit index stat fields and the full logical
// size used for content validation. Git must agree with the cached fields
// to leave a cloned file alone during checkout.
type fileStat struct {
	CtimeSec, CtimeNsec uint32
	MtimeSec, MtimeNsec uint32
	Dev, Ino            uint32
	Uid, Gid            uint32
	Size                uint32 // Git's munged 32-bit size
	FullSize            int64  // logical size used to hash the entire file
	mode                uint32 // st_mode, for our own checks; not stored in the index
}

func gitIndexSize(size int64) uint32 {
	truncated := uint32(size)
	if size != 0 && truncated == 0 {
		return 0x80000000
	}
	return truncated
}

func (fs fileStat) IsRegular() bool {
	const sIFMT, sIFREG = 0o170000, 0o100000
	return fs.mode&sIFMT == sIFREG
}

func (fs fileStat) IsExecutable() bool {
	return fs.mode&0o111 != 0
}
