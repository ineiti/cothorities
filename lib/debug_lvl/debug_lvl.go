package debug_lvl

import (
	"bytes"
	"flag"
	"fmt"
	"github.com/Sirupsen/logrus"
	"os"
	"regexp"
	"runtime"
)

// These are information-debugging levels that can be turned on or off.
// Every logging greater than 'DebugVisible' will be discarded. So you can
// Log at different levels and easily turn on or off the amount of logging
// generated by adjusting the 'DebugVisible' variable.
var DebugVisible = 1

// The padding of functions to make a nice debug-output - this is automatically updated
// whenever there are longer functions and kept at that new maximum. If you prefer
// to have a fixed output and don't remember oversized names, put a negative value
// in here
var NamePadding = 40

// Padding of line-numbers for a nice debug-output - used in the same way as
// NamePadding
var LinePadding = 3

// Testing output has to be on fmt, it doesn't take into account log-outputs
// So for testing, set Testing = true, and instead of sending to log, it will
// output to fmt
var Testing = false

// If this variable is set, it will be outputted between the position and the message
var StaticMsg = ""

// Holds the logrus-structure to do our logging
var DebugLog = &logrus.Logger{
	Out:       os.Stdout,
	Formatter: &DebugLvl{},
	Hooks:     make(logrus.LevelHooks),
	Level:     logrus.InfoLevel}

var regexpPaths, _ = regexp.Compile(".*/")

func init() {
	flag.IntVar(&DebugVisible, "debug", DebugVisible, "How much debug you from 1 (discrete) - 5 (very noisy). Default 1")
}

func Lvl(lvl int, args ...interface{}) {
	pc, _, line, _ := runtime.Caller(2)
	name := regexpPaths.ReplaceAllString(runtime.FuncForPC(pc).Name(), "")
	lineStr := fmt.Sprintf("%d", line)

	// For the testing-framework, we check the resulting string. So as not to
	// have the tests fail every time somebody moves the functions, we put
	// the line-# to 0
	if Testing {
		line = 0
	}

	if len(name) > NamePadding && NamePadding > 0 {
		NamePadding = len(name)
	}
	if len(lineStr) > LinePadding && LinePadding > 0 {
		LinePadding = len(name)
	}
	fmtstr := fmt.Sprintf("%%%ds: %%%dd", NamePadding, LinePadding)
	caller := fmt.Sprintf(fmtstr, name, line)
	if StaticMsg != "" {
		caller += "@" + StaticMsg
	}
	DebugLog.WithFields(logrus.Fields{
		"debug_lvl": lvl,
		"caller":    caller}).Println(args...)
}

func Lvlf(lvl int, f string, args ...interface{}) {
	Lvl(lvl, fmt.Sprintf(f, args...))
}

func Print(args ...interface{}) {
	Lvl(-1, args...)
}

func Printf(f string, args ...interface{}) {
	Lvlf(-1, f, args...)
}

func Lvl1(args ...interface{}) {
	Lvl(1, args...)
}

func Lvl2(args ...interface{}) {
	Lvl(2, args...)
}

func Lvl3(args ...interface{}) {
	Lvl(3, args...)
}

func Lvl4(args ...interface{}) {
	Lvl(4, args...)
}

func Lvl5(args ...interface{}) {
	Lvl(5, args...)
}

func Fatal(args ...interface{}) {
	Lvl(0, args...)
	os.Exit(1)
}

func Panic(args ...interface{}) {
	Lvl(0, args...)
	panic(args)
}

func Lvlf1(f string, args ...interface{}) {
	Lvlf(1, f, args...)
}

func Lvlf2(f string, args ...interface{}) {
	Lvlf(2, f, args...)
}

func Lvlf3(f string, args ...interface{}) {
	Lvlf(3, f, args...)
}

func Lvlf4(f string, args ...interface{}) {
	Lvlf(4, f, args...)
}

func Lvlf5(f string, args ...interface{}) {
	Lvlf(5, f, args...)
}

func Fatalf(f string, args ...interface{}) {
	Lvlf(0, f, args...)
	os.Exit(1)
}

func Panicf(f string, args ...interface{}) {
	Lvlf(0, f, args...)
	panic(args)
}

// To easy print a debug-message anyway without discarding the level
// Just add an additional "L" in front, and remove it later:
// - easy hack to turn on other debug-messages
// - easy removable by searching/replacing 'LLvl' with 'Lvl'
func LLvl1(args ...interface{})            { Lvl(-1, args...) }
func LLvl2(args ...interface{})            { Lvl(-1, args...) }
func LLvl3(args ...interface{})            { Lvl(-1, args...) }
func LLvl4(args ...interface{})            { Lvl(-1, args...) }
func LLvl5(args ...interface{})            { Lvl(-1, args...) }
func LLvlf1(f string, args ...interface{}) { Lvlf(-1, f, args...) }
func LLvlf2(f string, args ...interface{}) { Lvlf(-1, f, args...) }
func LLvlf3(f string, args ...interface{}) { Lvlf(-1, f, args...) }
func LLvlf4(f string, args ...interface{}) { Lvlf(-1, f, args...) }
func LLvlf5(f string, args ...interface{}) { Lvlf(-1, f, args...) }

type DebugLvl struct {
}

func (f *DebugLvl) Format(entry *logrus.Entry) ([]byte, error) {
	lvl := entry.Data["debug_lvl"].(int)
	caller := entry.Data["caller"].(string)
	if lvl <= DebugVisible {
		b := &bytes.Buffer{}
		b.WriteString(fmt.Sprintf("%d: (%s) - %s", lvl, caller, entry.Message))
		b.WriteByte('\n')

		if Testing {
			fmt.Print(b)
			return nil, nil
		} else {
			return b.Bytes(), nil
		}
	} else {
		if len(entry.Message) > 2048 && DebugVisible > 1 {
			fmt.Printf("%d: (%s) - HUGE message of %d bytes not printed\n", lvl, caller, len(entry.Message))
		}
		return nil, nil
	}
}
