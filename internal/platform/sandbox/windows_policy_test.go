package sandbox

import (
	"context"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

// parseWindowsCommandLine is an independent model of CommandLineToArgvW
// (and the Microsoft C runtime since 2008) used to check quoting on every
// build: the program name ends at the closing quote or first whitespace;
// 2n backslashes before a quote are n backslashes and a delimiter, 2n+1 are
// n backslashes and a literal quote; "" inside a quoted span is a quote.
func parseWindowsCommandLine(line string) []string {
	var result []string
	i := 0
	var program strings.Builder
	if strings.HasPrefix(line, `"`) {
		i = 1
		for i < len(line) && line[i] != '"' {
			program.WriteByte(line[i])
			i++
		}
		i++
	} else {
		for i < len(line) && line[i] != ' ' && line[i] != '\t' {
			program.WriteByte(line[i])
			i++
		}
	}
	result = append(result, program.String())
	for {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			return result
		}
		var argument strings.Builder
		quoted := false
		for i < len(line) && (quoted || line[i] != ' ' && line[i] != '\t') {
			switch line[i] {
			case '\\':
				count := 0
				for i < len(line) && line[i] == '\\' {
					count++
					i++
				}
				if i < len(line) && line[i] == '"' {
					argument.WriteString(strings.Repeat(`\`, count/2))
					if count%2 == 1 {
						argument.WriteByte('"')
						i++
					}
				} else {
					argument.WriteString(strings.Repeat(`\`, count))
				}
			case '"':
				if quoted && i+1 < len(line) && line[i+1] == '"' {
					argument.WriteByte('"')
					i += 2
				} else {
					quoted = !quoted
					i++
				}
			default:
				argument.WriteByte(line[i])
				i++
			}
		}
		result = append(result, argument.String())
	}
}

func TestWindowsCommandLineGoldenQuoting(t *testing.T) {
	program := `C:\Jelee\.tools\ffprobe.exe`
	for argument, want := range map[string]string{
		"":              `""`,
		"plain":         `plain`,
		"a b":           `"a b"`,
		"tab\there":     "\"tab\there\"",
		`a\b`:           `a\b`,
		`a\\b`:          `a\\b`,
		`a"b`:           `a\"b`,
		`a\"b`:          `a\\\"b`,
		`trailing\`:     `trailing\`,
		`space\ end\`:   `"space\ end\\"`,
		`"quoted"`:      `\"quoted\"`,
		`with "both" \`: `"with \"both\" \\"`,
		"%PATH%&|<>^":   `%PATH%&|<>^`,
		"影片 測試":         `"影片 測試"`,
	} {
		line, ok := WindowsCommandLine([]string{program, argument})
		if !ok || line != `"`+program+`" `+want {
			t.Fatalf("%q: got %q", argument, line)
		}
	}
}

func TestWindowsCommandLineRoundTripsArbitraryArguments(t *testing.T) {
	alphabet := []byte("ab \t\"\\\\x-=:")
	random := rand.New(rand.NewPCG(9, 27)) //nolint:gosec // G404: deterministic test input, not security randomness
	for range 4000 {
		argv := []string{`C:\Program Files\Jelee\ffprobe.exe`}
		for range random.IntN(5) {
			value := make([]byte, random.IntN(9))
			for i := range value {
				value[i] = alphabet[random.IntN(len(alphabet))]
			}
			argv = append(argv, string(value))
		}
		line, ok := WindowsCommandLine(argv)
		if !ok {
			t.Fatalf("refused %q", argv)
		}
		if got := parseWindowsCommandLine(line); !slices.Equal(got, argv) {
			t.Fatalf("argv %q became %q via %q", argv, got, line)
		}
	}
	fixed := append([]string{`C:\Jelee\ffprobe.exe`}, metadataArguments()...)
	line, ok := WindowsCommandLine(fixed)
	if !ok || !slices.Equal(parseWindowsCommandLine(line), fixed) {
		t.Fatal("fixed metadata argv did not round-trip")
	}
}

func TestWindowsCommandLineRefusesAmbiguousInput(t *testing.T) {
	for name, argv := range map[string][]string{
		"empty":         nil,
		"empty program": {""},
		"quote program": {`C:\a"b.exe`},
		"nul program":   {"C:\\a\x00.exe"},
		"nul argument":  {`C:\a.exe`, "x\x00y"},
		"invalid utf8":  {`C:\a.exe`, "\xff"},
		"too long":      {`C:\a.exe`, strings.Repeat("x", 32767)},
	} {
		if line, ok := WindowsCommandLine(argv); ok || line != "" {
			t.Fatalf("%s accepted: %q", name, line)
		}
	}
}

func TestValidWindowsToolPath(t *testing.T) {
	for _, value := range []string{`C:\Jelee\.tools\bin\ffprobe.exe`, `d:\x\FFPROBE.EXE`, `C:\ffprobe.exe`} {
		if !ValidWindowsToolPath(value, "ffprobe.exe") {
			t.Fatalf("refused %q", value)
		}
	}
	for _, value := range []string{
		``, `ffprobe.exe`, `\ffprobe.exe`, `C:ffprobe.exe`, `C:/x/ffprobe.exe`, `\\server\share\ffprobe.exe`,
		`\\?\C:\x\ffprobe.exe`, `\\.\C:\x\ffprobe.exe`, `C:\x\..\ffprobe.exe`, `C:\x\.\ffprobe.exe`, `C:\x\\ffprobe.exe`,
		`C:\x\ffprobe.exe:stream`, `C:\x\ffprobe.exe.`, `C:\x\ffprobe.exe `, `C:\x \ffprobe.exe`, `C:\x\ffmpeg.exe`,
		`C:\x\ffprobe`, "C:\\x\n\\ffprobe.exe", `1:\x\ffprobe.exe`, `C:\x*\ffprobe.exe`, `C:\` + strings.Repeat("x", 260) + `\ffprobe.exe`,
	} {
		if ValidWindowsToolPath(value, "ffprobe.exe") {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestWindowsToolLocationUsesInstallationRootOnly(t *testing.T) {
	const relative = ".tools/media/windows-amd64/gyan-9.0.2/ffmpeg-9.0.2-essentials_build/bin/ffprobe.exe"
	got, ok := WindowsToolLocation(`C:\Jelee\bin\jelee.exe`, relative, "ffprobe.exe")
	if !ok || got != `C:\Jelee\.tools\media\windows-amd64\gyan-9.0.2\ffmpeg-9.0.2-essentials_build\bin\ffprobe.exe` {
		t.Fatalf("location %q %v", got, ok)
	}
	for _, input := range [][2]string{
		{`C:\jelee.exe`, relative},
		{`bin\jelee.exe`, relative},
		{`\\server\share\bin\jelee.exe`, relative},
		{`C:\Jelee\bin\jelee.exe`, "media/ffprobe.exe"},
		{`C:\Jelee\bin\jelee.exe`, ".tools/../ffprobe.exe"},
		{`C:\Jelee\bin\jelee.exe`, `.tools/x\ffprobe.exe`},
		{`C:\Jelee\bin\jelee.exe`, ".tools/media/ffmpeg.exe"},
		{`C:\Jelee\bin\jelee.exe`, "/.tools/ffprobe.exe"},
	} {
		if got, ok := WindowsToolLocation(input[0], input[1], "ffprobe.exe"); ok || got != "" {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestMetadataConfinementIsCompleteAndFresh(t *testing.T) {
	confinement := MetadataConfinement()
	if !confinement.Valid() || confinement.ActiveProcesses != 1 || confinement.MemoryBytes != 2<<30 || confinement.UserCPUTime != 30*time.Second ||
		confinement.IntegritySID != "S-1-16-4096" || confinement.UIRestrictions != 0xff || !slices.Contains(confinement.DenyOnlyGroups, "S-1-5-32-544") {
		t.Fatalf("confinement %+v", confinement)
	}
	confinement.DenyOnlyGroups[0] = "S-1-1-0"
	if MetadataConfinement().DenyOnlyGroups[0] != "S-1-5-32-544" {
		t.Fatal("confinement shared mutable state")
	}
	for name, weaken := range map[string]func(*Confinement){
		"memory":     func(c *Confinement) { c.MemoryBytes = 0 },
		"huge":       func(c *Confinement) { c.MemoryBytes = 8 << 30 },
		"cpu":        func(c *Confinement) { c.UserCPUTime = 0 },
		"processes":  func(c *Confinement) { c.ActiveProcesses = 2 },
		"ui":         func(c *Confinement) { c.UIRestrictions &^= 0x02 },
		"integrity":  func(c *Confinement) { c.IntegritySID = "S-1-16-8192" },
		"deny list":  func(c *Confinement) { c.DenyOnlyGroups = c.DenyOnlyGroups[1:] },
		"deny value": func(c *Confinement) { c.DenyOnlyGroups[0] = "S-1-5-32-545" },
		"zero":       func(c *Confinement) { *c = Confinement{} },
	} {
		value := MetadataConfinement()
		weaken(&value)
		if value.Valid() {
			t.Fatalf("%s: weakened confinement accepted", name)
		}
	}
}

func TestDirectLauncherExposesOnlyFixedToolArguments(t *testing.T) {
	direct := &Launcher{executable: `C:\Jelee\ffprobe.exe`, ffprobePath: `C:\Jelee\ffprobe.exe`, direct: true}
	if !direct.Direct() || direct.HelperArguments() != nil || direct.CoverHelperArguments(0) != nil || !direct.Confinement().Valid() {
		t.Fatal("direct launcher exposed a helper argv or no confinement")
	}
	arguments := direct.DirectArguments()
	if !slices.Equal(arguments, metadataArguments()) || arguments[len(arguments)-1] != "fd:" {
		t.Fatalf("direct argv %q", arguments)
	}
	arguments[0] = "-report"
	if direct.DirectArguments()[0] != "-hide_banner" {
		t.Fatal("direct argv mutable")
	}
	for stream := -1; stream <= CoverMaxVideoIndex+1; stream++ {
		got := direct.DirectCoverArguments(stream)
		if (stream < 0 || stream > CoverMaxVideoIndex) != (got == nil) || got != nil && !slices.Equal(got, coverArguments(stream)) {
			t.Fatalf("cover %d: %q", stream, got)
		}
	}
	helper := &Launcher{executable: "trusted", descriptor: "fixed"}
	var absent *Launcher
	for _, l := range []*Launcher{helper, absent} {
		if l.Direct() || l.DirectArguments() != nil || l.DirectCoverArguments(0) != nil || l.Confinement().Valid() || l.Close() != nil {
			t.Fatal("helper launcher exposed direct execution")
		}
	}
}

func TestDirectPolicyIsStaticDigestOnly(t *testing.T) {
	digest := strings.Repeat("a", 64)
	if !validDirectPolicy(Policy{FFprobeSHA256: digest}) {
		t.Fatal("static policy refused")
	}
	for _, policy := range []Policy{{}, {FFprobeSHA256: "wrong"}, {FFprobeSHA256: digest, Libraries: []PinnedFile{{Path: "/x", SHA256: digest}}}} {
		if validDirectPolicy(policy) {
			t.Fatal("direct policy accepted a closure or bad digest")
		}
	}
}

func TestDirectLauncherOnlyOnWindows(t *testing.T) {
	if directBuild {
		t.Skip("covered by the Windows direct launcher tests")
	}
	if l, err := newDirect(context.Background(), Profile{FFprobePath: `C:\x\ffprobe.exe`}, Policy{FFprobeSHA256: strings.Repeat("a", 64)}); err != ErrUnsupported || l != nil {
		t.Fatal("direct launcher available on a non-Windows build")
	}
}
