package timectl

import "os"

func Enable(start string) {
	os.Setenv("FAKETIME", start)
	os.Setenv("LD_PRELOAD", "/usr/lib/x86_64-linux-gnu/faketime/libfaketime.so.1")
}

func Advance(delta string) {
	os.Setenv("FAKETIME", "+"+delta)
}