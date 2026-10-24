package main

import "strings"

// brainfuckABProgram reads two two-digit nonnegative integers separated by a
// space. Decimal counters produce a sum from 0 through 198 without leading
// zeroes, including carries between digits. The generated source is Brainfuck
// only; the generator does not precompute any judge answers.
func brainfuckABProgram() string {
	const (
		sum           = 0
		input         = 1
		units         = 3
		tens          = 4
		hundreds      = 5
		unitCountdown = 6
		copy          = 7
		backup        = 8
		zeroFlag      = 9
		printTens     = 10
		tensCountdown = 11
	)
	var program strings.Builder
	position := 0
	move := func(cell int) {
		if cell > position {
			program.WriteString(strings.Repeat(">", cell-position))
		} else {
			program.WriteString(strings.Repeat("<", position-cell))
		}
		position = cell
	}
	add := func(cell, amount int) {
		move(cell)
		if amount > 0 {
			program.WriteString(strings.Repeat("+", amount))
		} else {
			program.WriteString(strings.Repeat("-", -amount))
		}
	}
	clear := func(cell int) {
		move(cell)
		program.WriteString("[-]")
	}
	copyCell := func(cell int) {
		move(cell)
		program.WriteString("[-")
		add(copy, 1)
		add(backup, 1)
		move(cell)
		program.WriteString("]")
		move(backup)
		program.WriteString("[-")
		add(cell, 1)
		move(backup)
		program.WriteString("]")
	}
	ifZero := func(cell int, body func()) {
		add(zeroFlag, 1)
		copyCell(cell)
		move(copy)
		program.WriteString("[")
		clear(copy)
		clear(zeroFlag)
		move(copy)
		program.WriteString("]")
		move(zeroFlag)
		program.WriteString("[-")
		body()
		move(zeroFlag)
		program.WriteString("]")
	}
	readDigit := func(weight int) {
		move(input)
		program.WriteString(",")
		add(input, -48)
		program.WriteString("[-")
		add(sum, weight)
		move(input)
		program.WriteString("]")
	}
	readDigit(10)
	readDigit(1)
	program.WriteString(",") // Discard the separating space.
	readDigit(10)
	readDigit(1)

	add(unitCountdown, 10)
	add(tensCountdown, 10)
	move(sum)
	program.WriteString("[-")
	add(units, 1)
	add(unitCountdown, -1)
	ifZero(unitCountdown, func() {
		clear(units)
		add(unitCountdown, 10)
		add(tens, 1)
		add(tensCountdown, -1)
		ifZero(tensCountdown, func() {
			clear(tens)
			add(tensCountdown, 10)
			add(hundreds, 1)
		})
	})
	move(sum)
	program.WriteString("]")

	move(hundreds)
	program.WriteString("[")
	add(hundreds, 48)
	program.WriteString(".")
	clear(hundreds)
	add(printTens, 1)
	move(hundreds)
	program.WriteString("]")
	copyCell(tens)
	move(copy)
	program.WriteString("[")
	clear(copy)
	clear(printTens)
	add(printTens, 1)
	move(copy)
	program.WriteString("]")
	move(printTens)
	program.WriteString("[-")
	add(tens, 48)
	program.WriteString(".")
	move(printTens)
	program.WriteString("]")
	add(units, 48)
	program.WriteString(".")
	clear(input)
	add(input, 10)
	program.WriteString(".")
	return program.String()
}
