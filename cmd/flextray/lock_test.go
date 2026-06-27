package main

import (
	"testing"
	"time"
)

func TestInstanceLockRejectsSecondOwner(t *testing.T) {
	name := "FlexConnectFlexTrayTest-" + time.Now().Format("20060102150405.000000000")

	first, alreadyRunning, err := acquireInstanceLock(name)
	if err != nil {
		t.Fatal(err)
	}
	if alreadyRunning {
		t.Fatal("first lock reported an existing instance")
	}
	defer first.Close()

	second, alreadyRunning, err := acquireInstanceLock(name)
	if err != nil {
		t.Fatal(err)
	}
	if !alreadyRunning {
		if second != nil {
			second.Close()
		}
		t.Fatal("second lock succeeded")
	}
}

func TestInstanceLockReleases(t *testing.T) {
	name := "FlexConnectFlexTrayTest-" + time.Now().Format("20060102150405.000000000")

	first, alreadyRunning, err := acquireInstanceLock(name)
	if err != nil {
		t.Fatal(err)
	}
	if alreadyRunning {
		t.Fatal("first lock reported an existing instance")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, alreadyRunning, err := acquireInstanceLock(name)
	if err != nil {
		t.Fatal(err)
	}
	if alreadyRunning {
		t.Fatal("released lock still reported an existing instance")
	}
	defer second.Close()
}
