// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build dragonfly || freebsd || linux || netbsd || openbsd
// +build dragonfly freebsd linux netbsd openbsd

package main

import (
	"cmd/internal/sys"
	"debug/elf"
	"fmt"
	"internal/testenv"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"text/template"
)

func getCCAndCCFLAGS(t *testing.T, env []string) (string, []string) {
	goTool := testenv.GoToolPath(t)
	cmd := exec.Command(goTool, "env", "CC")
	cmd.Env = env
	ccb, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	cc := strings.TrimSpace(string(ccb))

	cmd = exec.Command(goTool, "env", "GOGCCFLAGS")
	cmd.Env = env
	cflagsb, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	cflags := strings.Fields(string(cflagsb))

	return cc, cflags
}

var asmSource = `
	.section .text1,"ax"
s1:
	.byte 0
	.section .text2,"ax"
s2:
	.byte 0
`

var goSource = `
package main
func main() {}
`

// The linker used to crash if an ELF input file had multiple text sections
// with the same name.
func TestSectionsWithSameName(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveCGO(t)
	t.Parallel()

	objcopy, err := exec.LookPath("objcopy")
	if err != nil {
		t.Skipf("can't find objcopy: %v", err)
	}

	dir := t.TempDir()

	gopath := filepath.Join(dir, "GOPATH")
	env := append(os.Environ(), "GOPATH="+gopath)

	if err := ioutil.WriteFile(filepath.Join(dir, "go.mod"), []byte("module elf_test\n"), 0666); err != nil {
		t.Fatal(err)
	}

	asmFile := filepath.Join(dir, "x.s")
	if err := ioutil.WriteFile(asmFile, []byte(asmSource), 0444); err != nil {
		t.Fatal(err)
	}

	goTool := testenv.GoToolPath(t)
	cc, cflags := getCCAndCCFLAGS(t, env)

	asmObj := filepath.Join(dir, "x.o")
	t.Logf("%s %v -c -o %s %s", cc, cflags, asmObj, asmFile)
	if out, err := exec.Command(cc, append(cflags, "-c", "-o", asmObj, asmFile)...).CombinedOutput(); err != nil {
		t.Logf("%s", out)
		t.Fatal(err)
	}

	asm2Obj := filepath.Join(dir, "x2.syso")
	t.Logf("%s --rename-section .text2=.text1 %s %s", objcopy, asmObj, asm2Obj)
	if out, err := exec.Command(objcopy, "--rename-section", ".text2=.text1", asmObj, asm2Obj).CombinedOutput(); err != nil {
		t.Logf("%s", out)
		t.Fatal(err)
	}

	for _, s := range []string{asmFile, asmObj} {
		if err := os.Remove(s); err != nil {
			t.Fatal(err)
		}
	}

	goFile := filepath.Join(dir, "main.go")
	if err := ioutil.WriteFile(goFile, []byte(goSource), 0444); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(goTool, "build")
	cmd.Dir = dir
	cmd.Env = env
	t.Logf("%s build", goTool)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("%s", out)
		t.Fatal(err)
	}
}

var cSources35779 = []string{`
static int blah() { return 42; }
int Cfunc1() { return blah(); }
`, `
static int blah() { return 42; }
int Cfunc2() { return blah(); }
`,
}

// TestMinusRSymsWithSameName tests a corner case in the new
// loader. Prior to the fix this failed with the error 'loadelf:
// $WORK/b001/_pkg_.a(ldr.syso): duplicate symbol reference: blah in
// both main(.text) and main(.text)'. See issue #35779.
func TestMinusRSymsWithSameName(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveCGO(t)
	t.Parallel()

	dir := t.TempDir()

	gopath := filepath.Join(dir, "GOPATH")
	env := append(os.Environ(), "GOPATH="+gopath)

	if err := ioutil.WriteFile(filepath.Join(dir, "go.mod"), []byte("module elf_test\n"), 0666); err != nil {
		t.Fatal(err)
	}

	goTool := testenv.GoToolPath(t)
	cc, cflags := getCCAndCCFLAGS(t, env)

	objs := []string{}
	csrcs := []string{}
	for i, content := range cSources35779 {
		csrcFile := filepath.Join(dir, fmt.Sprintf("x%d.c", i))
		csrcs = append(csrcs, csrcFile)
		if err := ioutil.WriteFile(csrcFile, []byte(content), 0444); err != nil {
			t.Fatal(err)
		}

		obj := filepath.Join(dir, fmt.Sprintf("x%d.o", i))
		objs = append(objs, obj)
		t.Logf("%s %v -c -o %s %s", cc, cflags, obj, csrcFile)
		if out, err := exec.Command(cc, append(cflags, "-c", "-o", obj, csrcFile)...).CombinedOutput(); err != nil {
			t.Logf("%s", out)
			t.Fatal(err)
		}
	}

	sysoObj := filepath.Join(dir, "ldr.syso")
	t.Logf("%s %v -nostdlib -r -o %s %v", cc, cflags, sysoObj, objs)
	if out, err := exec.Command(cc, append(cflags, "-nostdlib", "-r", "-o", sysoObj, objs[0], objs[1])...).CombinedOutput(); err != nil {
		t.Logf("%s", out)
		t.Fatal(err)
	}

	cruft := [][]string{objs, csrcs}
	for _, sl := range cruft {
		for _, s := range sl {
			if err := os.Remove(s); err != nil {
				t.Fatal(err)
			}
		}
	}

	goFile := filepath.Join(dir, "main.go")
	if err := ioutil.WriteFile(goFile, []byte(goSource), 0444); err != nil {
		t.Fatal(err)
	}

	t.Logf("%s build", goTool)
	cmd := exec.Command(goTool, "build")
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("%s", out)
		t.Fatal(err)
	}
}

const pieSourceTemplate = `
package main

import "fmt"

// Force the creation of a lot of type descriptors that will go into
// the .data.rel.ro section.
{{range $index, $element := .}}var V{{$index}} interface{} = [{{$index}}]int{}
{{end}}

func main() {
{{range $index, $element := .}}	fmt.Println(V{{$index}})
{{end}}
}
`

func TestPIESize(t *testing.T) {
	testenv.MustHaveGoBuild(t)

	// We don't want to test -linkmode=external if cgo is not supported.
	// On some systems -buildmode=pie implies -linkmode=external, so just
	// always skip the test if cgo is not supported.
	testenv.MustHaveCGO(t)

	if !sys.BuildModeSupported(runtime.Compiler, "pie", runtime.GOOS, runtime.GOARCH) {
		t.Skip("-buildmode=pie not supported")
	}

	t.Parallel()

	tmpl := template.Must(template.New("pie").Parse(pieSourceTemplate))

	writeGo := func(t *testing.T, dir string) {
		f, err := os.Create(filepath.Join(dir, "pie.go"))
		if err != nil {
			t.Fatal(err)
		}

		// Passing a 100-element slice here will cause
		// pieSourceTemplate to create 100 variables with
		// different types.
		if err := tmpl.Execute(f, make([]byte, 100)); err != nil {
			t.Fatal(err)
		}

		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}

	for _, external := range []bool{false, true} {
		external := external

		name := "TestPieSize-"
		if external {
			name += "external"
		} else {
			name += "internal"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()

			writeGo(t, dir)

			binexe := filepath.Join(dir, "exe")
			binpie := filepath.Join(dir, "pie")
			if external {
				binexe += "external"
				binpie += "external"
			}

			build := func(bin, mode string) error {
				cmd := exec.Command(testenv.GoToolPath(t), "build", "-o", bin, "-buildmode="+mode)
				if external {
					cmd.Args = append(cmd.Args, "-ldflags=-linkmode=external")
				}
				cmd.Args = append(cmd.Args, "pie.go")
				cmd.Dir = dir
				t.Logf("%v", cmd.Args)
				out, err := cmd.CombinedOutput()
				if len(out) > 0 {
					t.Logf("%s", out)
				}
				if err != nil {
					t.Error(err)
				}
				return err
			}

			var errexe, errpie error
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				errexe = build(binexe, "exe")
			}()
			go func() {
				defer wg.Done()
				errpie = build(binpie, "pie")
			}()
			wg.Wait()
			if errexe != nil || errpie != nil {
				t.Fatal("link failed")
			}

			var sizeexe, sizepie uint64
			if fi, err := os.Stat(binexe); err != nil {
				t.Fatal(err)
			} else {
				sizeexe = uint64(fi.Size())
			}
			if fi, err := os.Stat(binpie); err != nil {
				t.Fatal(err)
			} else {
				sizepie = uint64(fi.Size())
			}

			elfexe, err := elf.Open(binexe)
			if err != nil {
				t.Fatal(err)
			}
			defer elfexe.Close()

			elfpie, err := elf.Open(binpie)
			if err != nil {
				t.Fatal(err)
			}
			defer elfpie.Close()

			// The difference in size between exe and PIE
			// should be approximately the difference in
			// size of the .text section plus the size of
			// the PIE dynamic data sections plus the
			// difference in size of the .got and .plt
			// sections if they exist.
			// We ignore unallocated sections.
			// There may be gaps between non-writeable and
			// writable PT_LOAD segments. We also skip those
			// gaps (see issue #36023).

			textsize := func(ef *elf.File, name string) uint64 {
				for _, s := range ef.Sections {
					if s.Name == ".text" {
						return s.Size
					}
				}
				t.Fatalf("%s: no .text section", name)
				return 0
			}
			textexe := textsize(elfexe, binexe)
			textpie := textsize(elfpie, binpie)

			dynsize := func(ef *elf.File) uint64 {
				var ret uint64
				for _, s := range ef.Sections {
					if s.Flags&elf.SHF_ALLOC == 0 {
						continue
					}
					switch s.Type {
					case elf.SHT_DYNSYM, elf.SHT_STRTAB, elf.SHT_REL, elf.SHT_RELA, elf.SHT_HASH, elf.SHT_GNU_HASH, elf.SHT_GNU_VERDEF, elf.SHT_GNU_VERNEED, elf.SHT_GNU_VERSYM:
						ret += s.Size
					}
					if s.Flags&elf.SHF_WRITE != 0 && (strings.Contains(s.Name, ".got") || strings.Contains(s.Name, ".plt")) {
						ret += s.Size
					}
				}
				return ret
			}

			dynexe := dynsize(elfexe)
			dynpie := dynsize(elfpie)

			extrasize := func(ef *elf.File) uint64 {
				var ret uint64
				// skip unallocated sections
				for _, s := range ef.Sections {
					if s.Flags&elf.SHF_ALLOC == 0 {
						ret += s.Size
					}
				}
				// also skip gaps between PT_LOAD segments
				var prev *elf.Prog
				for _, seg := range ef.Progs {
					if seg.Type != elf.PT_LOAD {
						continue
					}
					if prev != nil {
						ret += seg.Off - prev.Off - prev.Filesz
					}
					prev = seg
				}
				return ret
			}

			extraexe := extrasize(elfexe)
			extrapie := extrasize(elfpie)

			diffReal := (sizepie - extrapie) - (sizeexe - extraexe)
			diffExpected := (textpie + dynpie) - (textexe + dynexe)

			t.Logf("real size difference %#x, expected %#x", diffReal, diffExpected)

			if diffReal > (diffExpected + diffExpected/10) {
				t.Errorf("PIE unexpectedly large: got difference of %d (%d - %d), expected difference %d", diffReal, sizepie, sizeexe, diffExpected)
			}
		})
	}
}

func TestMappingSymbols(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("skipping arm64 only test")
	}

	testenv.MustHaveGoBuild(t)
	t.Parallel()

	buildmodes := []string{"exe", "pie"}

	var wg sync.WaitGroup
	wg.Add(len(buildmodes))

	for _, buildmode := range buildmodes {
		go func(mode string) {
			defer wg.Done()
			symbols := buildSymbols(t, mode)
			checkMappingSymbols(t, symbols)
		}(buildmode)
	}

	wg.Wait()
}

// Builds a simple program, then returns a corresponding symbol table for that binary
func buildSymbols(t *testing.T, mode string) []elf.Symbol {
	goTool := testenv.GoToolPath(t)

	dir := t.TempDir()

	goFile := filepath.Join(dir, "main.go")
	if err := ioutil.WriteFile(goFile, []byte(goSource), 0444); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(dir, "go.mod"), []byte("module elf_test\n"), 0666); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(goTool, "build", "-o", mode, "-buildmode="+mode)
	cmd.Dir = dir

	t.Logf("%s build", goTool)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("%s", out)
		t.Fatal(err)
	}

	bin := filepath.Join(dir, mode)

	elfexe, err := elf.Open(bin)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := elfexe.Symbols()
	if err != nil {
		t.Fatal(err)
	}

	return symbols
}

// Checks that mapping symbols are inserted correctly inside a symbol table.
func checkMappingSymbols(t *testing.T, symbols []elf.Symbol) {
	// mappingSymbols variable keeps only "$x" and "$d" symbols sorted by their position.
	var mappingSymbols []elf.Symbol
	for _, symbol := range symbols {
		if symbol.Name == "$x" || symbol.Name == "$d" {
			if elf.ST_TYPE(symbol.Info) != elf.STT_NOTYPE || elf.ST_BIND(symbol.Info) != elf.STB_LOCAL {
				t.Fatalf("met \"%v\" symbol at %v position with incorrect info %v", symbol.Name, symbol.Value, symbol.Info)
			}
			mappingSymbols = append(mappingSymbols, symbol)
		}
	}
	sort.Slice(mappingSymbols, func(i, j int) bool {
		return mappingSymbols[i].Value < mappingSymbols[j].Value
	})

	if len(mappingSymbols) == 0 {
		t.Fatal("binary does not have mapping symbols")
	}

	for i := 0; i < len(mappingSymbols)-1; i += 2 {
		if mappingSymbols[i].Name == "$d" {
			t.Fatalf("met unexpected \"$d\" symbol at %v position", mappingSymbols[i].Value)
		}
		if i+1 < len(mappingSymbols) && mappingSymbols[i+1].Name == "$x" {
			t.Fatalf("met unexpected \"$x\" symbol at %v position", mappingSymbols[i+1].Value)
		}
	}
}
