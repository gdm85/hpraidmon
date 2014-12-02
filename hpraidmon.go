/*
 * hpraidmon v0.1.0 - Monitor status of HP RAID controllers by parsing output of hpacucli
 * Copyright (C) 2014 gdm85 - https://github.com/gdm85/hpraidmon/

This program is free software; you can redistribute it and/or
modify it under the terms of the GNU General Public License
as published by the Free Software Foundation; either version 2
of the License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program; if not, write to the Free Software
Foundation, Inc., 51 Franklin Street, Fifth Floor, Boston, MA  02110-1301, USA.
*/

package main

import (
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Nagios-compatible exit codes
const (
	STATE_OK        = 0
	STATE_WARNING   = 1
	STATE_CRITICAL  = 2
	STATE_UNKNOWN   = 3
	STATE_DEPENDENT = 4
)

type StorageEnclosureProcessor struct {
	VendorId string
	Model    string
	Expander uint
	WWID     string
}

type Controller struct {
	Name         string
	Type         string
	Slot         uint
	SerialNumber string
	SEP          StorageEnclosureProcessor
	Arrays       []Array
	CurrentArray *Array
}

type Array struct {
	Id          rune
	Type        string
	UnusedSpace uint64
	Drives      []Drive
}

type Drive struct {
	Id       string // index or port:box:bay id, might be redundant
	RaidMode string
	Status   string
	Size     uint64
	Physical bool
	// below properties are set only for physical drives
	Type string
	Port string
	Box  uint
	Bay  uint
}

// output-tailored regular expressions
var ctlRx *regexp.Regexp = regexp.MustCompile("^(.*?) in Slot (\\d+) \\(([^\\)]+)\\)\\s+\\(sn: ([^\\)]+)\\)$")
var sepRx *regexp.Regexp = regexp.MustCompile("^SEP\\s+\\(Vendor ID\\s+([^,]+),\\s+Model  ([^\\)]+)\\)\\s+(\\d+)\\s+\\(WWID:\\s+([^\\)]+)\\)$")
var arrRx *regexp.Regexp = regexp.MustCompile("^array\\s+([A-Z])\\s+\\(([^,]+),\\s+Unused\\s+Space:([^\\)]+)\\)$")
var szRx *regexp.Regexp = regexp.MustCompile("^\\s*((\\d+)(\\.\\d+)?)\\s+((K|M|G|T)B)?$")
var logRx *regexp.Regexp = regexp.MustCompile("^(\\d+)\\s+\\(([^,]+),\\s+([^,]+),\\s+([^\\)]+)\\)$")
var physRx *regexp.Regexp = regexp.MustCompile("^([^\\s]+)\\s+\\(port\\s+([^:]+):box\\s+([^:]+):bay\\s+(\\d+),\\s+([^,]+),\\s+([^,]+),\\s+([^\\)]+)\\)$")

func (ctl *Controller) Describe() string {
	return fmt.Sprintf("%s in slot %d", ctl.Name, ctl.Slot)
}

func (arr *Array) Describe() string {
	return fmt.Sprintf("%c (%s)", arr.Id, arr.Type)
}

func logn(n, b float64) float64 {
	return math.Log(n) / math.Log(b)
}

// this function comes from https://github.com/dustin/go-humanize/blob/master/bytes.go
// under MIT license
func convertBytesToHumanReadable(s uint64) string {
	base := float64(1000)

	sizes := []string{"", "KB", "MB", "GB", "TB", "PB", "EB"}
	if s < 10 {
		return fmt.Sprintf("%d", s)
	}
	e := math.Floor(logn(float64(s), base))
	suffix := sizes[int(e)]
	val := math.Floor(float64(s)/math.Pow(base, e)*10+0.5) / 10
	f := "%.0f%s"
	if val < 10 {
		f = "%.1f%s"
	}
	return fmt.Sprintf(f, val, suffix)
}

func (d *Drive) Describe() string {
	var driveType, mode string
	if d.Physical {
		driveType = "physical"
		mode = d.Type
	} else {
		driveType = "logical"
		mode = d.RaidMode
	}

	return fmt.Sprintf("%s %s (%s, %s)", driveType, d.Id, mode, convertBytesToHumanReadable(d.Size))
}

func ControllerParse(s string) *Controller {
	var ctl Controller

	matched := ctlRx.FindStringSubmatch(s)

	ctl.Name = matched[1]
	ui, err := strconv.ParseUint(matched[2], 10, 32)
	if err != nil {
		panic(err)
	}
	ctl.Slot = uint(ui)
	ctl.Type = matched[3]
	ctl.SerialNumber = matched[4]

	return &ctl
}

func convertHumanReadableToBytes(s string) uint64 {
	matched := szRx.FindStringSubmatch(s)
	if len(matched) == 0 {
		panic("no match for " + s)
	}
	n, _ := strconv.ParseFloat(matched[1], 64)

	var mul uint64 = 1
	switch matched[5][0] {
	case 'E':
		mul *= 1000
		fallthrough
	case 'P':
		mul *= 1000
		fallthrough
	case 'T':
		mul *= 1000
		fallthrough
	case 'G':
		mul *= 1000
		fallthrough
	case 'M':
		mul *= 1000
		fallthrough
	case 'K':
		mul *= 1000
	default:
		panic("Unknown size prefix")
	}

	return uint64(n * float64(mul))
}

func ArrayParse(s string) *Array {
	var arr Array

	matched := arrRx.FindStringSubmatch(s)
	arr.Id = rune(matched[1][0])
	arr.Type = matched[2]
	arr.UnusedSpace = convertHumanReadableToBytes(matched[3])

	return &arr
}

func DriveParse(s string) *Drive {
	var d Drive
	if strings.HasPrefix(s, "logicaldrive") {
		matched := logRx.FindStringSubmatch(s[len("logicaldrive")+1:])

		d.Id = matched[1]
		d.Size = convertHumanReadableToBytes(matched[2])
		d.RaidMode = matched[3]
		d.Status = matched[4]
		d.Physical = false
	} else if strings.HasPrefix(s, "physicaldrive") {
		matched := physRx.FindStringSubmatch(s[len("physicaldrive")+1:])

		d.Id = matched[1]
		d.Port = matched[2]
		ui, err := strconv.ParseUint(matched[3], 10, 32)
		if err != nil {
			panic(err)
		}
		d.Box = uint(ui)
		ui, err = strconv.ParseUint(matched[4], 10, 32)
		if err != nil {
			panic(err)
		}
		d.Bay = uint(ui)
		d.Type = matched[5]
		d.Size = convertHumanReadableToBytes(matched[6])
		d.Status = matched[7]
		d.Physical = true
	} else {
		panic("cannot determine drive type")
	}

	return &d
}

func (ctl *Controller) Add(a *Array) {
	ctl.Arrays = append(ctl.Arrays, *a)
	ctl.CurrentArray = &ctl.Arrays[len(ctl.Arrays)-1]
}

func (arr *Array) Add(d *Drive) {
	arr.Drives = append(arr.Drives, *d)
}

func (sep *StorageEnclosureProcessor) Parse(s string) {
	matched := sepRx.FindStringSubmatch(s)
	sep.VendorId = matched[1]
	sep.Model = matched[2]
	ui, err := strconv.ParseUint(matched[3], 10, 32)
	if err != nil {
		panic(err)
	}
	sep.Expander = uint(ui)
	sep.WWID = matched[4]
}

func main() {
	bytes, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		panic(err)
	}

	var currentController *Controller
	var controllers []*Controller

	for lineNo, line := range strings.Split(string(bytes), "\n") {
		if len(line) == 0 {
			continue
		}

		// count number of trailing spaces
		var i int
		for i = 0; i < len(line); i++ {
			if line[i] != ' ' {
				break
			}
		}

		switch i {
		case 0:
			currentController = ControllerParse(line[i:])

			// create unassigned array
			currentController.Arrays = []Array{
				Array{
					Id:   'U',
					Type: "unassigned",
				},
			}

			controllers = append(controllers, currentController)
			break
		case 3:
			if strings.HasPrefix(line[i:], "SEP") {
				currentController.SEP.Parse(line[i:])
			} else if line[i:] == "unassigned" {
				// already created for all controllers as currentController.Arrays[0]
			} else {
				currentController.Add(ArrayParse(line[i:]))
			}
		case 6:
			currentController.CurrentArray.Add(DriveParse(line[i:]))
			break
		default:
			panic(fmt.Sprintf("cannot parse line %d with %d trailing spaces:%s", lineNo, i, line))

		}
	}

	exitCode := STATE_OK

	// check that status of each drive (logical or physical) is OK
	for _, controller := range controllers {
		for _, array := range controller.Arrays {
			for _, drive := range array.Drives {
				if drive.Status != "OK" {
					// print informational message about this drive
					fmt.Fprintf(os.Stderr, "controller '%s', array '%s': drive '%s' status is %s\n", controller.Describe(), array.Describe(), drive.Describe(), drive.Status)

					// failures on disks that are not assigned are non-critical
					if array.Id == 'U' {
						exitCode = STATE_WARNING
					} else {
						// for some specific failure states, consider them not (yet) critical
						if drive.Status == "Predictive Failure" {
							exitCode = STATE_WARNING
						} else {
							// disk is not unassigned and this is not a predictive failure
							exitCode = STATE_CRITICAL
						}
					}
				}
			}
		}
	}

	os.Exit(exitCode)
}
