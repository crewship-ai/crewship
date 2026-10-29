//go:build linux && amd64

#include "textflag.h"

// func Getpid() int32
TEXT ·Getpid(SB), NOSPLIT, $0-4
	MOVL $20, AX
	INT  $0x80
	MOVL AX, ret+0(FP)
	RET
