#include "textflag.h"

// Linux amd64 clone: VM|FS|FILES|SIGHAND|THREAD|SYSVSEM|PARENT_SETTID|
// CHILD_SETTID|CHILD_CLEARTID. Children use no Go runtime, TLS or signal handler.
TEXT ·cloneThread(SB), NOSPLIT|NOFRAME, $0-32
	MOVQ stackTop+0(FP), SI
	MOVQ stop+8(FP), R12
	MOVQ tid+16(FP), DX
	MOVQ DX, R10
	XORQ R8, R8
	MOVQ $0x1350f00, DI
	MOVQ $56, AX
	SYSCALL
	CMPQ AX, $0
	JE child
	MOVQ AX, ret+24(FP)
	RET
child:
	CMPL 0(R12), $0
	JNE finished
	MOVQ R12, DI
	MOVQ $128, SI // FUTEX_WAIT_PRIVATE
	XORQ DX, DX
	XORQ R10, R10
	XORQ R8, R8
	XORQ R9, R9
	MOVQ $202, AX
	SYSCALL
	JMP child
finished:
	XORQ DI, DI
	MOVQ $60, AX // exit current thread; kernel clears CHILD_CLEARTID
	SYSCALL
	JMP finished
