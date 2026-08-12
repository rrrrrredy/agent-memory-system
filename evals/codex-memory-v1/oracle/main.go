package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "expected one sealed answer hash")
		os.Exit(2)
	}
	expected, err := hex.DecodeString(os.Args[1])
	actual, actualErr := os.ReadFile(".agentmem-eval/agent-message.txt")
	if err != nil || len(expected) != sha256.Size || actualErr != nil {
		fmt.Fprintln(os.Stderr, "required oracle input is unavailable")
		os.Exit(2)
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(string(actual))))
	if !equalBytes(digest[:], expected) {
		fmt.Fprintln(os.Stderr, "answer hash mismatch")
		os.Exit(1)
	}
	fmt.Println("exact answer matched")
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	difference := byte(0)
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}
