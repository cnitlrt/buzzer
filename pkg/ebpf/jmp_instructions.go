// Copyright 2023 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ebpf

import (
	"fmt"
)

// JmpInstruction represents the basic interface jmp instructions should implement
type JmpInstruction interface {
	Instruction
	GetFalseBranchSize() int16
	SetFalseBranchNextInstr(i Instruction)
	SetTrueBranchNextInstr(i Instruction)
}

// BaseJmpInstruction has the basic fields of a jmp instruction
type BaseJmpInstruction struct {

	// DstReg is from where the value to compare for the jump will be taken.
	DstReg *Register

	// TrueBranchNextInstr instruction that will be executed if the operation
	// evaluates to true.
	TrueBranchNextInstr Instruction
	trueBranchGenerator func(prog *Program) Instruction

	// FalseBranchNextInstr is the instruction that will be executed if
	// the operation evaluates to false.
	FalseBranchNextInstr Instruction

	// FalseBranchSize is how many instructions there are in the false
	// branch.
	FalseBranchSize      int16
	falseBranchGenerator func(prog *Program) (Instruction, int16)
}

// GetFalseBranchSize returns how many instructions there are in the false
// branch of the jmp instruction.
func (c *BaseJmpInstruction) GetFalseBranchSize() int16 {
	return c.FalseBranchSize
}

func (c *BaseJmpInstruction) SetFalseBranchNextInstr(i Instruction) {
	c.FalseBranchNextInstr = i
}
func (c *BaseJmpInstruction) SetTrueBranchNextInstr(i Instruction) {
	c.TrueBranchNextInstr = i
}

// GenerateNextInstruction uses the prog generator to create the rest of the tree.
func (c *BaseJmpInstruction) GenerateNextInstruction(prog *Program) {
	if c.falseBranchGenerator != nil {
		nextInstruction, bSize := c.falseBranchGenerator(prog)
		c.FalseBranchNextInstr = nextInstruction
		c.FalseBranchSize = bSize
	}

	if c.TrueBranchNextInstr != nil {
		c.TrueBranchNextInstr.GenerateNextInstruction(prog)
	} else if c.trueBranchGenerator != nil {
		c.TrueBranchNextInstr = c.trueBranchGenerator(prog)
	}
}

// NumerateInstruction sets the instruction number recursively
func (c *BaseJmpInstruction) NumerateInstruction(b *BaseInstruction, instrNo uint32) int {
	b.instructionNumber = instrNo
	instrNo++

	// This logic will result in us traversing the tree in dfs twice
	// (if we also need to traverse the true branch). But for now
	// this is the best idea I can come up with to numerate instructions
	// on conditional branches.
	if c.FalseBranchNextInstr != nil {
		c.FalseBranchNextInstr.NumerateInstruction(instrNo)
	}

	instrNo += uint32(c.FalseBranchSize)

	if c.TrueBranchNextInstr != nil {
		return 1 + int(c.FalseBranchSize) + c.TrueBranchNextInstr.NumerateInstruction(instrNo)
	}

	return 1 + int(c.FalseBranchSize)
}

// SetNextInstruction manually sets the next instruction.
func (c *BaseJmpInstruction) SetNextInstruction(next Instruction) {
	// For now, always pass the next instruction to the true branch.
	if c.TrueBranchNextInstr != nil {
		c.TrueBranchNextInstr.SetNextInstruction(next)
	} else {
		c.TrueBranchNextInstr = next
	}
}

// GetNextInstruction returns the next instruction, mostly used for testing
// purposes.
func (c *BaseJmpInstruction) GetNextInstruction() Instruction {
	// For now only return the true branch next instr.
	return c.TrueBranchNextInstr
}

// JmpImmInstruction Represents an eBPF jump (branching) operation.
type JmpImmInstruction struct {

	// Add all the basic things all instructions have.
	BaseInstruction

	// Add all the basic things for jmp instructions
	BaseJmpInstruction

	// Imm is the immediate value that will be used as the other comparing
	// operand.
	Imm int32
}

// GenerateNextInstruction uses the prog generator to create the rest of the tree.
func (c *JmpImmInstruction) GenerateNextInstruction(prog *Program) {
	c.BaseJmpInstruction.GenerateNextInstruction(prog)
}

// NumerateInstruction sets the instruction number recursively
func (c *JmpImmInstruction) NumerateInstruction(instrNo uint32) int {
	return c.BaseJmpInstruction.NumerateInstruction(&c.BaseInstruction, instrNo)
}

// SetNextInstruction manually sets the next instruction.
func (c *JmpImmInstruction) SetNextInstruction(next Instruction) {
	c.BaseJmpInstruction.SetNextInstruction(next)
}

// GetNextInstruction returns the next instruction, mostly used for testing
// purposes.
func (c *JmpImmInstruction) GetNextInstruction() Instruction {
	return c.BaseJmpInstruction.GetNextInstruction()
}

// GenerateBytecode generates the bytecode associated with this instruction.
func (c *JmpImmInstruction) GenerateBytecode() []uint64 {
	bytecode := []uint64{encodeImmediateJmpInstruction(c.Opcode, c.InstructionClass, c.DstReg.RegisterNumber(), c.Imm, c.FalseBranchSize)}
	if c.FalseBranchNextInstr != nil {
		// Only take the `c.FalseBranchSize` number of opcodes of the
		// false brach generated bytecode.
		falseBranchBytecode := c.FalseBranchNextInstr.GenerateBytecode()[0:c.FalseBranchSize]
		bytecode = append(bytecode, falseBranchBytecode...)
	}
	if c.TrueBranchNextInstr != nil {
		bytecode = append(bytecode, c.TrueBranchNextInstr.GenerateBytecode()...)
	}
	return bytecode
}

// GeneratePoc generates the C macros to repro this program.
func (c *JmpImmInstruction) GeneratePoc() []string {
	if c.Opcode == JmpExit {
		return []string{"BPF_EXIT_INSN()"}
	}
	var insClass string
	if c.InstructionClass == InsClassJmp {
		insClass = "BPF_JMP"
	} else {
		insClass = "BPF_JMP32"
	}
	insName := NameForJmpInstruction(c.Opcode)
	regName := c.DstReg.ToString()
	macro := fmt.Sprintf("BPF_JMP_IMM(%s, /*dst=*/%s, /*imm=*/%d, /*off=*/%d, /*ins_class=*/%s)", insName, regName, c.Imm, c.FalseBranchSize, insClass)
	r := []string{macro}
	if c.FalseBranchNextInstr != nil {
		r = append(r, c.FalseBranchNextInstr.GeneratePoc()...)
	}
	if c.TrueBranchNextInstr != nil {
		r = append(r, c.TrueBranchNextInstr.GeneratePoc()...)
	}
	return r
}

// CallInstruction represents a call to an ebpf auxiliary function.
type CallInstruction struct {

	// Add all the basic things all instructions have.
	BaseInstruction

	fnNumber int32
}

// GenerateBytecode generates the bytecode associated with this instruction.
func (c *CallInstruction) GenerateBytecode() []uint64 {
	bytecode := []uint64{encodeImmediateJmpInstruction(JmpCALL, InsClassJmp, UnusedField, c.fnNumber /*offset=*/, 0)}
	if c.nextInstruction != nil {
		bytecode = append(bytecode, c.nextInstruction.GenerateBytecode()...)
	}
	return bytecode
}

// GeneratePoc generates the C macros to repro this program.
func (c *CallInstruction) GeneratePoc() []string {
	macro := fmt.Sprintf("BPF_CALL_FUNC(%s)", GetBpfFuncName(c.fnNumber))
	r := []string{macro}
	if c.nextInstruction != nil {
		r = append(r, c.nextInstruction.GeneratePoc()...)
	}
	return r
}

// JmpRegInstruction Represents an eBPF jump (branching) operation.
type JmpRegInstruction struct {

	// Add all the basic things all instructions have.
	BaseInstruction

	// Add all the basic things for jmp instructions.
	BaseJmpInstruction

	// SrcReg holds the value that will be used as the other comparing
	// operand.
	SrcReg *Register
}

// GenerateNextInstruction uses the prog generator to create the rest of the tree.
func (c *JmpRegInstruction) GenerateNextInstruction(prog *Program) {
	c.BaseJmpInstruction.GenerateNextInstruction(prog)
}

// NumerateInstruction sets the instruction number recursively
func (c *JmpRegInstruction) NumerateInstruction(instrNo uint32) int {
	return c.BaseJmpInstruction.NumerateInstruction(&c.BaseInstruction, instrNo)
}

// SetNextInstruction manually sets the next instruction.
func (c *JmpRegInstruction) SetNextInstruction(next Instruction) {
	c.BaseJmpInstruction.SetNextInstruction(next)
}

// GetNextInstruction returns the next instruction, mostly used for testing
// purposes.
func (c *JmpRegInstruction) GetNextInstruction() Instruction {
	return c.BaseJmpInstruction.GetNextInstruction()
}

// GenerateBytecode generates the bytecode for this instruction.
func (c *JmpRegInstruction) GenerateBytecode() []uint64 {
	bytecode := []uint64{encodeRegisterJmpInstruction(c.Opcode, c.InstructionClass, c.DstReg.RegisterNumber(), c.SrcReg.RegisterNumber(), c.FalseBranchSize)}
	if c.FalseBranchNextInstr != nil {
		// Only take the `c.FalseBranchSize` number of opcodes of the
		// false brach generated bytecode.
		falseBranchBytecode := c.FalseBranchNextInstr.GenerateBytecode()[0:c.FalseBranchSize]
		bytecode = append(bytecode, falseBranchBytecode...)
	}
	if c.TrueBranchNextInstr != nil {
		bytecode = append(bytecode, c.TrueBranchNextInstr.GenerateBytecode()...)
	}
	return bytecode
}

// GeneratePoc generates the C macros to repro this program.
func (c *JmpRegInstruction) GeneratePoc() []string {
	var insClass string
	if c.InstructionClass == InsClassJmp {
		insClass = "BPF_JMP"
	} else {
		insClass = "BPF_JMP32"
	}
	insName := NameForJmpInstruction(c.Opcode)
	dstRegName := c.DstReg.ToString()
	srcRegName := c.SrcReg.ToString()
	macro := fmt.Sprintf("BPF_JMP_REG(%s, /*dst=*/%s, /*src=*/%s, /*off=*/%d, /*ins_class=*/%s)", insName, dstRegName, srcRegName, c.FalseBranchSize, insClass)
	r := []string{macro}
	if c.FalseBranchNextInstr != nil {
		r = append(r, c.FalseBranchNextInstr.GeneratePoc()...)
	}
	if c.TrueBranchNextInstr != nil {
		r = append(r, c.TrueBranchNextInstr.GeneratePoc()...)
	}
	return r
}

func newJmpInstruction(opcode, insclass uint8, dstReg *Register, src interface{}, offset int16) Instruction {
	isInt, srcInt := isIntType(src)
	if isInt {
		return &JmpImmInstruction{BaseInstruction: BaseInstruction{Opcode: opcode, InstructionClass: insclass}, BaseJmpInstruction: BaseJmpInstruction{DstReg: dstReg, FalseBranchSize: offset}, Imm: int32(srcInt)}
	} else if srcReg, ok := src.(*Register); ok {
		return &JmpRegInstruction{BaseInstruction: BaseInstruction{Opcode: opcode, InstructionClass: insclass}, BaseJmpInstruction: BaseJmpInstruction{DstReg: dstReg, FalseBranchSize: offset}, SrcReg: srcReg}
	}
	return nil
}

// Jmp represents an inconditional jump of `offset` instructions.
func Jmp(offset int16) Instruction {
	return newJmpInstruction(JmpJA, InsClassJmp, RegR0, UnusedField, offset)
}

func JmpEQ(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJEQ, InsClassJmp, dstReg, src, offset)
}

func JmpEQ32(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJEQ, InsClassJmp32, dstReg, src, offset)
}

func JmpGT(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJGT, InsClassJmp, dstReg, src, offset)
}

func JmpGT32(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJGT, InsClassJmp32, dstReg, src, offset)
}

func JmpGE(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJGE, InsClassJmp, dstReg, src, offset)
}

func JmpGE32(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJGE, InsClassJmp32, dstReg, src, offset)
}

func JmpSET(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJSET, InsClassJmp, dstReg, src, offset)
}

func JmpSET32(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJSET, InsClassJmp32, dstReg, src, offset)
}

func JmpNE(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJNE, InsClassJmp, dstReg, src, offset)
}

func JmpNE32(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJNE, InsClassJmp32, dstReg, src, offset)
}

func JmpSGT(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJSGT, InsClassJmp, dstReg, src, offset)
}

func JmpSGT32(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJSGT, InsClassJmp32, dstReg, src, offset)
}

func JmpSGE(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJSGE, InsClassJmp, dstReg, src, offset)
}

func JmpSGE32(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJSGE, InsClassJmp32, dstReg, src, offset)
}

// TODO: It would be nice if we can create wrappers for each call function
// something like:
// ```
// bpf_map_lookup_elemen(ptr_to_map, key)
// ```
// These functions would, ideally, set up the correct registers behind the
// scenes, for the example above it would be the equivalent of doing:
// ```
// Mov64(R1, ptr_to_map)
// Mov64(R2, key)
// CallFunction(map_lookup_element)
// ```
func Call(functionValue int32) Instruction {
	return &CallInstruction{fnNumber: functionValue}
}

// LdMapElement loads a map element ptr to R0.
// It does the following operations:
// - Set R1 to the pointer of the target map.
// - Stores `element` at keyPtr + offset: *(u32 *)(keyPtr + offset) = element
// - Sets R2 to hold (keyPtr + offset)
// - Calls map_lookup_element
func LdMapElement(mapPtr *Register, element int, keyPtr *Register, offset int16) Instruction {
	root, _ := InstructionSequence(
		Mov64(RegR1, mapPtr),
		StW(keyPtr, element, offset),
		Mov64(RegR2, keyPtr),
		Add64(RegR2, offset),
		Call(MapLookup),
	)
	return root
}

// CallSkbLoadBytesRelative sets up the state of the registers to invoke the
// skb_load_bytes_relative helper function.
//
// The invocation of this function would look more or less like this:
// skb_load_bytes_relative(skb, skb_offset, dstAddress + dstAddressOffset, length, start_header).
//
// All the interface{} parameters can be either integers or Registers.
func CallSkbLoadBytesRelative(skb *Register, skb_offset interface{}, dstAddress *Register, dstAddressOffset interface{}, length interface{}, start_header interface{}) Instruction {
	root, _ := InstructionSequence(
		Mov64(RegR1, skb),
		Mov64(RegR2, skb_offset),
		Mov64(RegR3, dstAddress),
		Add64(RegR3, dstAddressOffset),
		Mov64(RegR4, length),
		Mov64(RegR5, start_header),
		Call(SkbLoadBytesRelative),
	)
	return root
}

func Exit() Instruction {
	return newJmpInstruction(JmpExit, InsClassJmp, RegR0, UnusedField, UnusedField)
}

func JmpLT(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJLT, InsClassJmp, dstReg, src, offset)
}

func JmpLT32(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJLT, InsClassJmp32, dstReg, src, offset)
}

func JmpLE(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJLE, InsClassJmp, dstReg, src, offset)
}

func JmpLE32(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJLE, InsClassJmp32, dstReg, src, offset)
}

func JmpSLT(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJSLT, InsClassJmp, dstReg, src, offset)
}

func JmpSLT32(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJSLT, InsClassJmp32, dstReg, src, offset)
}

func JmpSLE(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJSLE, InsClassJmp, dstReg, src, offset)
}

func JmpSLE32(dstReg *Register, src interface{}, offset int16) Instruction {
	return newJmpInstruction(JmpJSLE, InsClassJmp32, dstReg, src, offset)
}
