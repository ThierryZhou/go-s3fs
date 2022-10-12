// Textual user interface parts of the config system

package config

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/ThierryZhou/go-s3fs/fs"
	"github.com/ThierryZhou/go-s3fs/fs/config/configmap"
	"github.com/ThierryZhou/go-s3fs/fs/config/configstruct"
	"github.com/ThierryZhou/go-s3fs/fs/driveletter"
	"github.com/ThierryZhou/go-s3fs/fs/fspath"
)

// ReadLine reads some input
var ReadLine = func() string {
	buf := bufio.NewReader(os.Stdin)
	line, err := buf.ReadString('\n')
	if err != nil {
		log.Fatalf("Failed to read line: %v", err)
	}
	return strings.TrimSpace(line)
}

// ReadNonEmptyLine prints prompt and calls Readline until non empty
func ReadNonEmptyLine(prompt string) string {
	result := ""
	for result == "" {
		fmt.Print(prompt)
		result = strings.TrimSpace(ReadLine())
	}
	return result
}

// CommandDefault - choose one.  If return is pressed then it will
// chose the defaultIndex if it is >= 0
func CommandDefault(commands []string, defaultIndex int) byte {
	opts := []string{}
	for i, text := range commands {
		def := ""
		if i == defaultIndex {
			def = " (default)"
		}
		fmt.Printf("%c) %s%s\n", text[0], text[1:], def)
		opts = append(opts, text[:1])
	}
	optString := strings.Join(opts, "")
	optHelp := strings.Join(opts, "/")
	for {
		fmt.Printf("%s> ", optHelp)
		result := strings.ToLower(ReadLine())
		if len(result) == 0 {
			if defaultIndex >= 0 {
				return optString[defaultIndex]
			}
			fmt.Printf("This value is required and it has no default.\n")
		} else if len(result) == 1 {
			i := strings.Index(optString, string(result[0]))
			if i >= 0 {
				return result[0]
			}
			fmt.Printf("This value must be one of the following characters: %s.\n", strings.Join(opts, ", "))
		} else {
			fmt.Printf("This value must be a single character, one of the following: %s.\n", strings.Join(opts, ", "))
		}
	}
}

// Command - choose one
func Command(commands []string) byte {
	return CommandDefault(commands, -1)
}

// Confirm asks the user for Yes or No and returns true or false
//
// If the user presses enter then the Default will be used
func Confirm(Default bool) bool {
	defaultIndex := 0
	if !Default {
		defaultIndex = 1
	}
	return CommandDefault([]string{"yYes", "nNo"}, defaultIndex) == 'y'
}

// Choose one of the choices, or default, or type a new string if newOk is set
func Choose(what string, kind string, choices, help []string, defaultValue string, required bool, newOk bool) string {
	valueDescription := "an existing"
	if newOk {
		valueDescription = "your own"
	}
	fmt.Printf("Choose a number from below, or type in %s %s.\n", valueDescription, kind)
	// Empty input is allowed if not required is set, or if
	// required is set but there is a default value to use.
	if defaultValue != "" {
		fmt.Printf("Press Enter for the default (%s).\n", defaultValue)
	} else if !required {
		fmt.Printf("Press Enter to leave empty.\n")
	}
	for {
		fmt.Printf("%s> ", what)
		result := ReadLine()
		i, err := strconv.Atoi(result)
		if err != nil {
			for _, v := range choices {
				if result == v {
					return result
				}
			}
			if result == "" {
				// If empty string is in the predefined list of choices it has already been returned above.
				// If parameter required is not set, then empty string is always a valid value.
				if !required {
					return result
				}
				// If parameter required is set, but there is a default, then empty input means default.
				if defaultValue != "" {
					return defaultValue
				}
				// If parameter required is set, and there is no default, then an input value is required.
				fmt.Printf("This value is required and it has no default.\n")
			} else if newOk {
				// If legal input is not restricted to defined choices, then any nonzero input string is accepted.
				return result
			} else {
				// A nonzero input string was specified but it did not match any of the strictly defined choices.
				fmt.Printf("This value must match %s value.\n", valueDescription)
			}
		} else {
			if i >= 1 && i <= len(choices) {
				return choices[i-1]
			}
			fmt.Printf("No choices with this number.\n")
		}
	}
}

// Enter prompts for an input value of a specified type
func Enter(what string, kind string, defaultValue string, required bool) string {
	// Empty input is allowed if not required is set, or if
	// required is set but there is a default value to use.
	fmt.Printf("Enter a %s.", kind)
	if defaultValue != "" {
		fmt.Printf(" Press Enter for the default (%s).\n", defaultValue)
	} else if !required {
		fmt.Println(" Press Enter to leave empty.")
	} else {
		fmt.Println()
	}
	for {
		fmt.Printf("%s> ", what)
		result := ReadLine()
		if !required || result != "" {
			return result
		}
		if defaultValue != "" {
			return defaultValue
		}
		fmt.Printf("This value is required and it has no default.\n")
	}
}

// ChooseNumber asks the user to enter a number between min and max
// inclusive prompting them with what.
func ChooseNumber(what string, min, max int) int {
	for {
		fmt.Printf("%s> ", what)
		result := ReadLine()
		i, err := strconv.Atoi(result)
		if err != nil {
			fmt.Printf("Bad number: %v\n", err)
			continue
		}
		if i < min || i > max {
			fmt.Printf("Out of range - %d to %d inclusive\n", min, max)
			continue
		}
		return i
	}
}

// mustFindByName finds the RegInfo for the remote name passed in or
// exits with a fatal error.
func mustFindByName(name string) *fs.RegInfo {
	fsType := FileGet(name, "type")
	if fsType == "" {
		log.Fatalf("Couldn't find type of fs for %q", name)
	}
	return fs.MustFind(fsType)
}

// newSection prints an empty line to separate sections
func newSection() {
	fmt.Println()
}

// backendConfig configures the backend starting from the state passed in
//
// The is the user interface loop that drives the post configuration backend config.
func backendConfig(ctx context.Context, name string, m configmap.Mapper, ri *fs.RegInfo, choices configmap.Getter, startState string) error {
	in := fs.ConfigIn{
		State: startState,
	}
	for {
		out, err := fs.BackendConfig(ctx, name, m, ri, choices, in)
		if err != nil {
			return err
		}
		if out == nil {
			break
		}
		if out.Error != "" {
			fmt.Println(out.Error)
		}
		in.State = out.State
		in.Result = out.Result
		if out.Option != nil {
			fs.Debugf(name, "config: reading config parameter %q", out.Option.Name)
			if out.Option.Default == nil {
				out.Option.Default = ""
			}
			if Default, isBool := out.Option.Default.(bool); isBool &&
				len(out.Option.Examples) == 2 &&
				out.Option.Examples[0].Help == "Yes" &&
				out.Option.Examples[0].Value == "true" &&
				out.Option.Examples[1].Help == "No" &&
				out.Option.Examples[1].Value == "false" &&
				out.Option.Exclusive {
				// Use Confirm for Yes/No questions as it has a nicer interface=
				fmt.Println(out.Option.Help)
				in.Result = fmt.Sprint(Confirm(Default))
			} else {
				value := ChooseOption(out.Option, name)
				if value != "" {
					err := out.Option.Set(value)
					if err != nil {
						return fmt.Errorf("failed to set option: %w", err)
					}
				}
				in.Result = out.Option.String()
			}
		}
		if out.State == "" {
			break
		}
		newSection()
	}
	return nil
}

// PostConfig configures the backend after the main config has been done
//
// The is the user interface loop that drives the post configuration backend config.
func PostConfig(ctx context.Context, name string, m configmap.Mapper, ri *fs.RegInfo) error {
	if ri.Config == nil {
		return errors.New("backend doesn't support reconnect or authorize")
	}
	return backendConfig(ctx, name, m, ri, configmap.Simple{}, "")
}

// RemoteConfig runs the config helper for the remote if needed
func RemoteConfig(ctx context.Context, name string) error {
	fmt.Printf("Remote config\n")
	ri := mustFindByName(name)
	m := fs.ConfigMap(ri, name, nil)
	if ri.Config == nil {
		return nil
	}
	return PostConfig(ctx, name, m, ri)
}

// ChooseOption asks the user to choose an option
func ChooseOption(o *fs.Option, name string) string {
	fmt.Printf("Option %s.\n", o.Name)
	if o.Help != "" {
		// Show help string without empty lines.
		help := strings.ReplaceAll(strings.TrimSpace(o.Help), "\n\n", "\n")
		fmt.Println(help)
	}

	var defaultValue string
	if o.Default == nil {
		defaultValue = ""
	} else {
		defaultValue = fmt.Sprint(o.Default)
	}

	what := "value"
	if o.Default != "" {
		switch o.Default.(type) {
		case bool:
			what = "boolean value (true or false)"
		case fs.SizeSuffix:
			what = "size with suffix K,M,G,T"
		case fs.Duration:
			what = "duration s,m,h,d,w,M,y"
		case int, int8, int16, int32, int64:
			what = "signed integer"
		case uint, byte, uint16, uint32, uint64:
			what = "unsigned integer"
		default:
			what = fmt.Sprintf("%T value", o.Default)
		}
	}
	var in string
	for {
		if len(o.Examples) > 0 {
			var values []string
			var help []string
			for _, example := range o.Examples {
				values = append(values, example.Value)
				help = append(help, example.Help)
			}
			in = Choose(o.Name, what, values, help, defaultValue, o.Required, !o.Exclusive)
		} else {
			in = Enter(o.Name, what, defaultValue, o.Required)
		}
		if in != "" {
			newIn, err := configstruct.StringToInterface(o.Default, in)
			if err != nil {
				fmt.Printf("Failed to parse %q: %v\n", in, err)
				continue
			}
			in = fmt.Sprint(newIn) // canonicalise
		}
		return in
	}
}

// NewRemoteName asks the user for a name for a new remote
func NewRemoteName() (name string) {
	for {
		fmt.Println("Enter name for new remote.")
		fmt.Printf("name> ")
		name = ReadLine()
		if LoadedData().HasSection(name) {
			fmt.Printf("Remote %q already exists.\n", name)
			continue
		}
		err := fspath.CheckConfigName(name)
		switch {
		case name == "":
			fmt.Printf("Can't use empty name.\n")
		case driveletter.IsDriveLetter(name):
			fmt.Printf("Can't use %q as it can be confused with a drive letter.\n", name)
		case err != nil:
			fmt.Printf("Can't use %q as %v.\n", name, err)
		default:
			return name
		}
	}
}

// ShowConfigLocation prints the location of the config file in use
func ShowConfigLocation() {
	if configPath := GetConfigPath(); configPath == "" {
		fmt.Println("Configuration is in memory only")
	} else {
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			fmt.Println("Configuration file doesn't exist, but rclone will use this path:")
		} else {
			fmt.Println("Configuration file is stored at:")
		}
		fmt.Printf("%s\n", configPath)
	}
}

// ShowConfig prints the (unencrypted) config options
func ShowConfig() {
	str, err := LoadedData().Serialize()
	if err != nil {
		log.Fatalf("Failed to serialize config: %v", err)
	}
	if str == "" {
		str = "; empty config\n"
	}
	fmt.Printf("%s", str)
}

// Suppress the confirm prompts by altering the context config
func suppressConfirm(ctx context.Context) context.Context {
	newCtx, ci := fs.AddConfig(ctx)
	ci.AutoConfirm = true
	return newCtx
}
