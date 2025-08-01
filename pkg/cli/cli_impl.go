package cli

// This file implements the public CLI. It's deliberately implemented using
// esbuild's public "Build", "Transform", and "AnalyzeMetafile" APIs instead of
// using internal APIs so that any tests that cover the CLI also implicitly
// cover the public API as well.

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/evanw/esbuild/internal/cli_helpers"
	"github.com/evanw/esbuild/internal/fs"
	"github.com/evanw/esbuild/internal/logger"
	"github.com/evanw/esbuild/pkg/api"
)

func newBuildOptions() api.BuildOptions {
	return api.BuildOptions{
		Banner:      make(map[string]string),
		Define:      make(map[string]string),
		Footer:      make(map[string]string),
		Loader:      make(map[string]api.Loader),
		LogOverride: make(map[string]api.LogLevel),
		Supported:   make(map[string]bool),
	}
}

func newTransformOptions() api.TransformOptions {
	return api.TransformOptions{
		Define:      make(map[string]string),
		LogOverride: make(map[string]api.LogLevel),
		Supported:   make(map[string]bool),
	}
}

type parseOptionsKind uint8

const (
	// This means we're parsing it for our own internal use
	kindInternal parseOptionsKind = iota

	// This means the result is returned through a public API
	kindExternal
)

type parseOptionsExtras struct {
	watch       bool
	watchDelay  int
	metafile    *string
	mangleCache *string
}

func isBoolFlag(arg string, flag string) bool {
	if strings.HasPrefix(arg, flag) {
		remainder := arg[len(flag):]
		return len(remainder) == 0 || remainder[0] == '='
	}
	return false
}

func parseBoolFlag(arg string, defaultValue bool) (bool, *cli_helpers.ErrorWithNote) {
	equals := strings.IndexByte(arg, '=')
	if equals == -1 {
		return defaultValue, nil
	}
	value := arg[equals+1:]
	switch value {
	case "false":
		return false, nil
	case "true":
		return true, nil
	}
	return false, cli_helpers.MakeErrorWithNote(
		fmt.Sprintf("Invalid value %q in %q", value, arg),
		"Valid values are \"true\" or \"false\".",
	)
}

func parseOptionsImpl(
	osArgs []string,
	buildOpts *api.BuildOptions,
	transformOpts *api.TransformOptions,
	kind parseOptionsKind,
) (extras parseOptionsExtras, err *cli_helpers.ErrorWithNote) {
	hasBareSourceMapFlag := false

	// Parse the arguments now that we know what we're parsing
	for _, arg := range osArgs {
		switch {
		case isBoolFlag(arg, "--bundle") && buildOpts != nil:
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else {
				buildOpts.Bundle = value
			}

		case isBoolFlag(arg, "--preserve-symlinks") && buildOpts != nil:
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else {
				buildOpts.PreserveSymlinks = value
			}

		case isBoolFlag(arg, "--splitting") && buildOpts != nil:
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else {
				buildOpts.Splitting = value
			}

		case isBoolFlag(arg, "--allow-overwrite") && buildOpts != nil:
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else {
				buildOpts.AllowOverwrite = value
			}

		case isBoolFlag(arg, "--watch") && buildOpts != nil:
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else {
				extras.watch = value
			}

		case strings.HasPrefix(arg, "--watch-delay=") && buildOpts != nil:
			value := arg[len("--watch-delay="):]
			delay, err := strconv.Atoi(value)
			if err != nil {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Invalid value %q in %q", value, arg),
					"The watch delay must be an integer.",
				)
			}
			extras.watchDelay = delay

		case isBoolFlag(arg, "--minify"):
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else if buildOpts != nil {
				buildOpts.MinifySyntax = value
				buildOpts.MinifyWhitespace = value
				buildOpts.MinifyIdentifiers = value
			} else {
				transformOpts.MinifySyntax = value
				transformOpts.MinifyWhitespace = value
				transformOpts.MinifyIdentifiers = value
			}

		case isBoolFlag(arg, "--minify-syntax"):
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else if buildOpts != nil {
				buildOpts.MinifySyntax = value
			} else {
				transformOpts.MinifySyntax = value
			}

		case isBoolFlag(arg, "--minify-whitespace"):
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else if buildOpts != nil {
				buildOpts.MinifyWhitespace = value
			} else {
				transformOpts.MinifyWhitespace = value
			}

		case isBoolFlag(arg, "--minify-identifiers"):
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else if buildOpts != nil {
				buildOpts.MinifyIdentifiers = value
			} else {
				transformOpts.MinifyIdentifiers = value
			}

		case isBoolFlag(arg, "--mangle-quoted"):
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else {
				var mangleQuoted *api.MangleQuoted
				if buildOpts != nil {
					mangleQuoted = &buildOpts.MangleQuoted
				} else {
					mangleQuoted = &transformOpts.MangleQuoted
				}
				if value {
					*mangleQuoted = api.MangleQuotedTrue
				} else {
					*mangleQuoted = api.MangleQuotedFalse
				}
			}

		case strings.HasPrefix(arg, "--mangle-props="):
			value := arg[len("--mangle-props="):]
			if buildOpts != nil {
				buildOpts.MangleProps = value
			} else {
				transformOpts.MangleProps = value
			}

		case strings.HasPrefix(arg, "--reserve-props="):
			value := arg[len("--reserve-props="):]
			if buildOpts != nil {
				buildOpts.ReserveProps = value
			} else {
				transformOpts.ReserveProps = value
			}

		case strings.HasPrefix(arg, "--mangle-cache=") && buildOpts != nil && kind == kindInternal:
			value := arg[len("--mangle-cache="):]
			extras.mangleCache = &value

		case strings.HasPrefix(arg, "--drop:"):
			value := arg[len("--drop:"):]
			switch value {
			case "console":
				if buildOpts != nil {
					buildOpts.Drop |= api.DropConsole
				} else {
					transformOpts.Drop |= api.DropConsole
				}
			case "debugger":
				if buildOpts != nil {
					buildOpts.Drop |= api.DropDebugger
				} else {
					transformOpts.Drop |= api.DropDebugger
				}
			default:
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Invalid value %q in %q", value, arg),
					"Valid values are \"console\" or \"debugger\".",
				)
			}

		case strings.HasPrefix(arg, "--drop-labels="):
			if buildOpts != nil {
				buildOpts.DropLabels = splitWithEmptyCheck(arg[len("--drop-labels="):], ",")
			} else {
				transformOpts.DropLabels = splitWithEmptyCheck(arg[len("--drop-labels="):], ",")
			}

		case strings.HasPrefix(arg, "--legal-comments="):
			value := arg[len("--legal-comments="):]
			var legalComments api.LegalComments
			switch value {
			case "none":
				legalComments = api.LegalCommentsNone
			case "inline":
				legalComments = api.LegalCommentsInline
			case "eof":
				legalComments = api.LegalCommentsEndOfFile
			case "linked":
				legalComments = api.LegalCommentsLinked
			case "external":
				legalComments = api.LegalCommentsExternal
			default:
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Invalid value %q in %q", value, arg),
					"Valid values are \"none\", \"inline\", \"eof\", \"linked\", or \"external\".",
				)
			}
			if buildOpts != nil {
				buildOpts.LegalComments = legalComments
			} else {
				transformOpts.LegalComments = legalComments
			}

		case strings.HasPrefix(arg, "--charset="):
			var value *api.Charset
			if buildOpts != nil {
				value = &buildOpts.Charset
			} else {
				value = &transformOpts.Charset
			}
			name := arg[len("--charset="):]
			switch name {
			case "ascii":
				*value = api.CharsetASCII
			case "utf8":
				*value = api.CharsetUTF8
			default:
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Invalid value %q in %q", name, arg),
					"Valid values are \"ascii\" or \"utf8\".",
				)
			}

		case isBoolFlag(arg, "--tree-shaking"):
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else {
				var treeShaking *api.TreeShaking
				if buildOpts != nil {
					treeShaking = &buildOpts.TreeShaking
				} else {
					treeShaking = &transformOpts.TreeShaking
				}
				if value {
					*treeShaking = api.TreeShakingTrue
				} else {
					*treeShaking = api.TreeShakingFalse
				}
			}

		case isBoolFlag(arg, "--ignore-annotations"):
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else if buildOpts != nil {
				buildOpts.IgnoreAnnotations = value
			} else {
				transformOpts.IgnoreAnnotations = value
			}

		case isBoolFlag(arg, "--keep-names"):
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else if buildOpts != nil {
				buildOpts.KeepNames = value
			} else {
				transformOpts.KeepNames = value
			}

		case arg == "--sourcemap":
			if buildOpts != nil {
				buildOpts.Sourcemap = api.SourceMapLinked
			} else {
				transformOpts.Sourcemap = api.SourceMapInline
			}
			hasBareSourceMapFlag = true

		case strings.HasPrefix(arg, "--sourcemap="):
			value := arg[len("--sourcemap="):]
			var sourcemap api.SourceMap
			switch value {
			case "linked":
				sourcemap = api.SourceMapLinked
			case "inline":
				sourcemap = api.SourceMapInline
			case "external":
				sourcemap = api.SourceMapExternal
			case "both":
				sourcemap = api.SourceMapInlineAndExternal
			default:
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Invalid value %q in %q", value, arg),
					"Valid values are \"linked\", \"inline\", \"external\", or \"both\".",
				)
			}
			if buildOpts != nil {
				buildOpts.Sourcemap = sourcemap
			} else {
				transformOpts.Sourcemap = sourcemap
			}
			hasBareSourceMapFlag = false

		case strings.HasPrefix(arg, "--source-root="):
			sourceRoot := arg[len("--source-root="):]
			if buildOpts != nil {
				buildOpts.SourceRoot = sourceRoot
			} else {
				transformOpts.SourceRoot = sourceRoot
			}

		case isBoolFlag(arg, "--sources-content"):
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else {
				var sourcesContent *api.SourcesContent
				if buildOpts != nil {
					sourcesContent = &buildOpts.SourcesContent
				} else {
					sourcesContent = &transformOpts.SourcesContent
				}
				if value {
					*sourcesContent = api.SourcesContentInclude
				} else {
					*sourcesContent = api.SourcesContentExclude
				}
			}

		case strings.HasPrefix(arg, "--sourcefile="):
			if buildOpts != nil {
				if buildOpts.Stdin == nil {
					buildOpts.Stdin = &api.StdinOptions{}
				}
				buildOpts.Stdin.Sourcefile = arg[len("--sourcefile="):]
			} else {
				transformOpts.Sourcefile = arg[len("--sourcefile="):]
			}

		case strings.HasPrefix(arg, "--resolve-extensions=") && buildOpts != nil:
			buildOpts.ResolveExtensions = splitWithEmptyCheck(arg[len("--resolve-extensions="):], ",")

		case strings.HasPrefix(arg, "--main-fields=") && buildOpts != nil:
			buildOpts.MainFields = splitWithEmptyCheck(arg[len("--main-fields="):], ",")

		case strings.HasPrefix(arg, "--conditions=") && buildOpts != nil:
			buildOpts.Conditions = splitWithEmptyCheck(arg[len("--conditions="):], ",")

		case strings.HasPrefix(arg, "--public-path=") && buildOpts != nil:
			buildOpts.PublicPath = arg[len("--public-path="):]

		case strings.HasPrefix(arg, "--global-name="):
			if buildOpts != nil {
				buildOpts.GlobalName = arg[len("--global-name="):]
			} else {
				transformOpts.GlobalName = arg[len("--global-name="):]
			}

		case arg == "--metafile" && buildOpts != nil && kind == kindExternal:
			buildOpts.Metafile = true

		case strings.HasPrefix(arg, "--metafile=") && buildOpts != nil && kind == kindInternal:
			value := arg[len("--metafile="):]
			buildOpts.Metafile = true
			extras.metafile = &value

		case strings.HasPrefix(arg, "--outfile=") && buildOpts != nil:
			buildOpts.Outfile = arg[len("--outfile="):]

		case strings.HasPrefix(arg, "--outdir=") && buildOpts != nil:
			buildOpts.Outdir = arg[len("--outdir="):]

		case strings.HasPrefix(arg, "--outbase=") && buildOpts != nil:
			buildOpts.Outbase = arg[len("--outbase="):]

		case strings.HasPrefix(arg, "--tsconfig=") && buildOpts != nil:
			buildOpts.Tsconfig = arg[len("--tsconfig="):]

		case strings.HasPrefix(arg, "--tsconfig-raw="):
			if buildOpts != nil {
				buildOpts.TsconfigRaw = arg[len("--tsconfig-raw="):]
			} else {
				transformOpts.TsconfigRaw = arg[len("--tsconfig-raw="):]
			}

		case strings.HasPrefix(arg, "--entry-names=") && buildOpts != nil:
			buildOpts.EntryNames = arg[len("--entry-names="):]

		case strings.HasPrefix(arg, "--chunk-names=") && buildOpts != nil:
			buildOpts.ChunkNames = arg[len("--chunk-names="):]

		case strings.HasPrefix(arg, "--asset-names=") && buildOpts != nil:
			buildOpts.AssetNames = arg[len("--asset-names="):]

		case strings.HasPrefix(arg, "--define:"):
			value := arg[len("--define:"):]
			equals := strings.IndexByte(value, '=')
			if equals == -1 {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Missing \"=\" in %q", arg),
					"You need to use \"=\" to specify both the original value and the replacement value. "+
						"For example, \"--define:DEBUG=true\" replaces \"DEBUG\" with \"true\".",
				)
			}
			if buildOpts != nil {
				buildOpts.Define[value[:equals]] = value[equals+1:]
			} else {
				transformOpts.Define[value[:equals]] = value[equals+1:]
			}

		case strings.HasPrefix(arg, "--log-override:"):
			value := arg[len("--log-override:"):]
			equals := strings.IndexByte(value, '=')
			if equals == -1 {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Missing \"=\" in %q", arg),
					"You need to use \"=\" to specify both the message name and the log level. "+
						"For example, \"--log-override:css-syntax-error=error\" turns all \"css-syntax-error\" log messages into errors.",
				)
			}
			logLevel, err := parseLogLevel(value[equals+1:], arg)
			if err != nil {
				return parseOptionsExtras{}, err
			}
			if buildOpts != nil {
				buildOpts.LogOverride[value[:equals]] = logLevel
			} else {
				transformOpts.LogOverride[value[:equals]] = logLevel
			}

		case strings.HasPrefix(arg, "--abs-paths="):
			values := splitWithEmptyCheck(arg[len("--abs-paths="):], ",")
			var absPaths api.AbsPaths
			for _, value := range values {
				switch value {
				case "code":
					absPaths |= api.CodeAbsPath
				case "log":
					absPaths |= api.LogAbsPath
				case "metafile":
					absPaths |= api.MetafileAbsPath
				default:
					return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
						fmt.Sprintf("Invalid value %q in %q", value, arg),
						"Valid values are \"code\", \"log\", or \"metafile\".",
					)
				}
			}
			if buildOpts != nil {
				buildOpts.AbsPaths = absPaths
			} else {
				transformOpts.AbsPaths = absPaths
			}

		case strings.HasPrefix(arg, "--supported:"):
			value := arg[len("--supported:"):]
			equals := strings.IndexByte(value, '=')
			if equals == -1 {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Missing \"=\" in %q", arg),
					"You need to use \"=\" to specify both the name of the feature and whether it is supported or not. "+
						"For example, \"--supported:arrow=false\" marks arrow functions as unsupported.",
				)
			}
			if isSupported, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else if buildOpts != nil {
				buildOpts.Supported[value[:equals]] = isSupported
			} else {
				transformOpts.Supported[value[:equals]] = isSupported
			}

		case strings.HasPrefix(arg, "--pure:"):
			value := arg[len("--pure:"):]
			if buildOpts != nil {
				buildOpts.Pure = append(buildOpts.Pure, value)
			} else {
				transformOpts.Pure = append(transformOpts.Pure, value)
			}

		case strings.HasPrefix(arg, "--loader:") && buildOpts != nil:
			value := arg[len("--loader:"):]
			equals := strings.IndexByte(value, '=')
			if equals == -1 {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Missing \"=\" in %q", arg),
					"You need to specify the file extension that the loader applies to. "+
						"For example, \"--loader:.js=jsx\" applies the \"jsx\" loader to files with the \".js\" extension.",
				)
			}
			ext, text := value[:equals], value[equals+1:]
			loader, err := cli_helpers.ParseLoader(text)
			if err != nil {
				return parseOptionsExtras{}, err
			}
			buildOpts.Loader[ext] = loader

		case strings.HasPrefix(arg, "--loader="):
			value := arg[len("--loader="):]
			loader, err := cli_helpers.ParseLoader(value)
			if err != nil {
				return parseOptionsExtras{}, err
			}
			if loader == api.LoaderFile || loader == api.LoaderCopy {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("%q is not supported when transforming stdin", arg),
					fmt.Sprintf("Using esbuild to transform stdin only generates one output file, so you cannot use the %q loader "+
						"since that needs to generate two output files.", value),
				)
			}
			if buildOpts != nil {
				if buildOpts.Stdin == nil {
					buildOpts.Stdin = &api.StdinOptions{}
				}
				buildOpts.Stdin.Loader = loader
			} else {
				transformOpts.Loader = loader
			}

		case strings.HasPrefix(arg, "--target="):
			target, engines, err := parseTargets(splitWithEmptyCheck(arg[len("--target="):], ","), arg)
			if err != nil {
				return parseOptionsExtras{}, err
			}
			if buildOpts != nil {
				buildOpts.Target = target
				buildOpts.Engines = engines
			} else {
				transformOpts.Target = target
				transformOpts.Engines = engines
			}

		case strings.HasPrefix(arg, "--out-extension:") && buildOpts != nil:
			value := arg[len("--out-extension:"):]
			equals := strings.IndexByte(value, '=')
			if equals == -1 {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Missing \"=\" in %q", arg),
					"You need to use either \"--out-extension:.js=...\" or \"--out-extension:.css=...\" "+
						"to specify the file type that the output extension applies to .",
				)
			}
			if buildOpts.OutExtension == nil {
				buildOpts.OutExtension = make(map[string]string)
			}
			buildOpts.OutExtension[value[:equals]] = value[equals+1:]

		case strings.HasPrefix(arg, "--platform="):
			value := arg[len("--platform="):]
			var platform api.Platform
			switch value {
			case "browser":
				platform = api.PlatformBrowser
			case "node":
				platform = api.PlatformNode
			case "neutral":
				platform = api.PlatformNeutral
			default:
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Invalid value %q in %q", value, arg),
					"Valid values are \"browser\", \"node\", or \"neutral\".",
				)
			}
			if buildOpts != nil {
				buildOpts.Platform = platform
			} else {
				transformOpts.Platform = platform
			}

		case strings.HasPrefix(arg, "--format="):
			value := arg[len("--format="):]
			var format api.Format
			switch value {
			case "iife":
				format = api.FormatIIFE
			case "cjs":
				format = api.FormatCommonJS
			case "esm":
				format = api.FormatESModule
			default:
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Invalid value %q in %q", value, arg),
					"Valid values are \"iife\", \"cjs\", or \"esm\".",
				)
			}
			if buildOpts != nil {
				buildOpts.Format = format
			} else {
				transformOpts.Format = format
			}

		case strings.HasPrefix(arg, "--packages=") && buildOpts != nil:
			value := arg[len("--packages="):]
			var packages api.Packages
			switch value {
			case "bundle":
				packages = api.PackagesBundle
			case "external":
				packages = api.PackagesExternal
			default:
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Invalid value %q in %q", value, arg),
					"Valid values are \"bundle\" or \"external\".",
				)
			}
			buildOpts.Packages = packages

		case strings.HasPrefix(arg, "--external:") && buildOpts != nil:
			buildOpts.External = append(buildOpts.External, arg[len("--external:"):])

		case strings.HasPrefix(arg, "--inject:") && buildOpts != nil:
			buildOpts.Inject = append(buildOpts.Inject, arg[len("--inject:"):])

		case strings.HasPrefix(arg, "--alias:") && buildOpts != nil:
			value := arg[len("--alias:"):]
			equals := strings.IndexByte(value, '=')
			if equals == -1 {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Missing \"=\" in %q", arg),
					"You need to use \"=\" to specify both the original package name and the replacement package name. "+
						"For example, \"--alias:old=new\" replaces package \"old\" with package \"new\".",
				)
			}
			if buildOpts.Alias == nil {
				buildOpts.Alias = make(map[string]string)
			}
			buildOpts.Alias[value[:equals]] = value[equals+1:]

		case strings.HasPrefix(arg, "--jsx="):
			value := arg[len("--jsx="):]
			var mode api.JSX
			switch value {
			case "transform":
				mode = api.JSXTransform
			case "preserve":
				mode = api.JSXPreserve
			case "automatic":
				mode = api.JSXAutomatic
			default:
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Invalid value %q in %q", value, arg),
					"Valid values are \"transform\", \"automatic\", or \"preserve\".",
				)
			}
			if buildOpts != nil {
				buildOpts.JSX = mode
			} else {
				transformOpts.JSX = mode
			}

		case strings.HasPrefix(arg, "--jsx-factory="):
			value := arg[len("--jsx-factory="):]
			if buildOpts != nil {
				buildOpts.JSXFactory = value
			} else {
				transformOpts.JSXFactory = value
			}

		case strings.HasPrefix(arg, "--jsx-fragment="):
			value := arg[len("--jsx-fragment="):]
			if buildOpts != nil {
				buildOpts.JSXFragment = value
			} else {
				transformOpts.JSXFragment = value
			}

		case strings.HasPrefix(arg, "--jsx-import-source="):
			value := arg[len("--jsx-import-source="):]
			if buildOpts != nil {
				buildOpts.JSXImportSource = value
			} else {
				transformOpts.JSXImportSource = value
			}

		case isBoolFlag(arg, "--jsx-dev"):
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else if buildOpts != nil {
				buildOpts.JSXDev = value
			} else {
				transformOpts.JSXDev = value
			}

		case isBoolFlag(arg, "--jsx-side-effects"):
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else if buildOpts != nil {
				buildOpts.JSXSideEffects = value
			} else {
				transformOpts.JSXSideEffects = value
			}

		case strings.HasPrefix(arg, "--banner=") && transformOpts != nil:
			transformOpts.Banner = arg[len("--banner="):]

		case strings.HasPrefix(arg, "--footer=") && transformOpts != nil:
			transformOpts.Footer = arg[len("--footer="):]

		case strings.HasPrefix(arg, "--banner:") && buildOpts != nil:
			value := arg[len("--banner:"):]
			equals := strings.IndexByte(value, '=')
			if equals == -1 {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Missing \"=\" in %q", arg),
					"You need to use either \"--banner:js=...\" or \"--banner:css=...\" to specify the language that the banner applies to.",
				)
			}
			buildOpts.Banner[value[:equals]] = value[equals+1:]

		case strings.HasPrefix(arg, "--footer:") && buildOpts != nil:
			value := arg[len("--footer:"):]
			equals := strings.IndexByte(value, '=')
			if equals == -1 {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Missing \"=\" in %q", arg),
					"You need to use either \"--footer:js=...\" or \"--footer:css=...\" to specify the language that the footer applies to.",
				)
			}
			buildOpts.Footer[value[:equals]] = value[equals+1:]

		case strings.HasPrefix(arg, "--log-limit="):
			value := arg[len("--log-limit="):]
			limit, err := strconv.Atoi(value)
			if err != nil || limit < 0 {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Invalid value %q in %q", value, arg),
					"The log limit must be a non-negative integer.",
				)
			}
			if buildOpts != nil {
				buildOpts.LogLimit = limit
			} else {
				transformOpts.LogLimit = limit
			}

		case strings.HasPrefix(arg, "--line-limit="):
			value := arg[len("--line-limit="):]
			limit, err := strconv.Atoi(value)
			if err != nil || limit < 0 {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
					fmt.Sprintf("Invalid value %q in %q", value, arg),
					"The line limit must be a non-negative integer.",
				)
			}
			if buildOpts != nil {
				buildOpts.LineLimit = limit
			} else {
				transformOpts.LineLimit = limit
			}

			// Make sure this stays in sync with "PrintErrorToStderr"
		case isBoolFlag(arg, "--color"):
			if value, err := parseBoolFlag(arg, true); err != nil {
				return parseOptionsExtras{}, err
			} else {
				var color *api.StderrColor
				if buildOpts != nil {
					color = &buildOpts.Color
				} else {
					color = &transformOpts.Color
				}
				if value {
					*color = api.ColorAlways
				} else {
					*color = api.ColorNever
				}
			}

		// Make sure this stays in sync with "PrintErrorToStderr"
		case strings.HasPrefix(arg, "--log-level="):
			value := arg[len("--log-level="):]
			logLevel, err := parseLogLevel(value, arg)
			if err != nil {
				return parseOptionsExtras{}, err
			}
			if buildOpts != nil {
				buildOpts.LogLevel = logLevel
			} else {
				transformOpts.LogLevel = logLevel
			}

		case strings.HasPrefix(arg, "'--"):
			return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
				fmt.Sprintf("Unexpected single quote character before flag: %s", arg),
				"This typically happens when attempting to use single quotes to quote arguments with a shell that doesn't recognize single quotes. "+
					"Try using double quote characters to quote arguments instead.",
			)

		case !strings.HasPrefix(arg, "-") && buildOpts != nil:
			if equals := strings.IndexByte(arg, '='); equals != -1 {
				buildOpts.EntryPointsAdvanced = append(buildOpts.EntryPointsAdvanced, api.EntryPoint{
					OutputPath: arg[:equals],
					InputPath:  arg[equals+1:],
				})
			} else {
				buildOpts.EntryPoints = append(buildOpts.EntryPoints, arg)
			}

		default:
			bare := map[string]bool{
				"allow-overwrite":    true,
				"bundle":             true,
				"ignore-annotations": true,
				"jsx-dev":            true,
				"jsx-side-effects":   true,
				"keep-names":         true,
				"minify-identifiers": true,
				"minify-syntax":      true,
				"minify-whitespace":  true,
				"minify":             true,
				"preserve-symlinks":  true,
				"sourcemap":          true,
				"splitting":          true,
				"watch":              true,
			}

			equals := map[string]bool{
				"abs-paths":          true,
				"allow-overwrite":    true,
				"asset-names":        true,
				"banner":             true,
				"bundle":             true,
				"certfile":           true,
				"charset":            true,
				"chunk-names":        true,
				"color":              true,
				"conditions":         true,
				"cors-origin":        true,
				"drop-labels":        true,
				"entry-names":        true,
				"footer":             true,
				"format":             true,
				"global-name":        true,
				"ignore-annotations": true,
				"jsx-factory":        true,
				"jsx-fragment":       true,
				"jsx-import-source":  true,
				"jsx":                true,
				"keep-names":         true,
				"keyfile":            true,
				"legal-comments":     true,
				"loader":             true,
				"log-level":          true,
				"log-limit":          true,
				"main-fields":        true,
				"mangle-cache":       true,
				"mangle-props":       true,
				"mangle-quoted":      true,
				"metafile":           true,
				"minify-identifiers": true,
				"minify-syntax":      true,
				"minify-whitespace":  true,
				"minify":             true,
				"outbase":            true,
				"outdir":             true,
				"outfile":            true,
				"packages":           true,
				"platform":           true,
				"preserve-symlinks":  true,
				"public-path":        true,
				"reserve-props":      true,
				"resolve-extensions": true,
				"serve-fallback":     true,
				"serve":              true,
				"servedir":           true,
				"source-root":        true,
				"sourcefile":         true,
				"sourcemap":          true,
				"sources-content":    true,
				"splitting":          true,
				"target":             true,
				"tree-shaking":       true,
				"tsconfig-raw":       true,
				"tsconfig":           true,
				"watch":              true,
				"watch-delay":        true,
			}

			colon := map[string]bool{
				"alias":         true,
				"banner":        true,
				"define":        true,
				"drop":          true,
				"external":      true,
				"footer":        true,
				"inject":        true,
				"loader":        true,
				"log-override":  true,
				"out-extension": true,
				"pure":          true,
				"supported":     true,
			}

			note := ""

			// Try to provide helpful hints when we can recognize the mistake
			switch {
			case arg == "-o":
				note = "Use \"--outfile=\" to configure the output file instead of \"-o\"."

			case arg == "-v":
				note = "Use \"--log-level=verbose\" to generate verbose logs instead of \"-v\"."

			case strings.HasPrefix(arg, "--"):
				if i := strings.IndexByte(arg, '='); i != -1 && colon[arg[2:i]] {
					note = fmt.Sprintf("Use %q instead of %q. Flags that can be re-specified multiple times use \":\" instead of \"=\".",
						arg[:i]+":"+arg[i+1:], arg)
				}

				if i := strings.IndexByte(arg, ':'); i != -1 && equals[arg[2:i]] {
					note = fmt.Sprintf("Use %q instead of %q. Flags that can only be specified once use \"=\" instead of \":\".",
						arg[:i]+"="+arg[i+1:], arg)
				}

			case strings.HasPrefix(arg, "-"):
				isValid := bare[arg[1:]]
				fix := "-" + arg

				if i := strings.IndexByte(arg, '='); i != -1 && equals[arg[1:i]] {
					isValid = true
				} else if i != -1 && colon[arg[1:i]] {
					isValid = true
					fix = fmt.Sprintf("-%s:%s", arg[:i], arg[i+1:])
				} else if i := strings.IndexByte(arg, ':'); i != -1 && colon[arg[1:i]] {
					isValid = true
				} else if i != -1 && equals[arg[1:i]] {
					isValid = true
					fix = fmt.Sprintf("-%s=%s", arg[:i], arg[i+1:])
				}

				if isValid {
					note = fmt.Sprintf("Use %q instead of %q. Flags are always specified with two dashes instead of one dash.",
						fix, arg)
				}
			}

			if buildOpts != nil {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(fmt.Sprintf("Invalid build flag: %q", arg), note)
			} else {
				return parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(fmt.Sprintf("Invalid transform flag: %q", arg), note)
			}
		}
	}

	// If we're building, the last source map flag is "--sourcemap", and there
	// is no output path, change the source map option to "inline" because we're
	// going to be writing to stdout which can only represent a single file.
	if buildOpts != nil && hasBareSourceMapFlag && buildOpts.Outfile == "" && buildOpts.Outdir == "" {
		buildOpts.Sourcemap = api.SourceMapInline
	}

	return
}

func parseTargets(targets []string, arg string) (target api.Target, engines []api.Engine, err *cli_helpers.ErrorWithNote) {
	validTargets := map[string]api.Target{
		"esnext": api.ESNext,
		"es5":    api.ES5,
		"es6":    api.ES2015,
		"es2015": api.ES2015,
		"es2016": api.ES2016,
		"es2017": api.ES2017,
		"es2018": api.ES2018,
		"es2019": api.ES2019,
		"es2020": api.ES2020,
		"es2021": api.ES2021,
		"es2022": api.ES2022,
		"es2023": api.ES2023,
		"es2024": api.ES2024,
	}

outer:
	for _, value := range targets {
		if valid, ok := validTargets[strings.ToLower(value)]; ok {
			target = valid
			continue
		}

		for engine, name := range validEngines {
			if strings.HasPrefix(value, engine) {
				version := value[len(engine):]
				if version == "" {
					return 0, nil, cli_helpers.MakeErrorWithNote(
						fmt.Sprintf("Target %q is missing a version number in %q", value, arg),
						"",
					)
				}
				engines = append(engines, api.Engine{Name: name, Version: version})
				continue outer
			}
		}

		engines := make([]string, 0, len(validEngines))
		engines = append(engines, "\"esN\"")
		for key := range validEngines {
			engines = append(engines, fmt.Sprintf("%q", key+"N"))
		}
		sort.Strings(engines)
		return 0, nil, cli_helpers.MakeErrorWithNote(
			fmt.Sprintf("Invalid target %q in %q", value, arg),
			fmt.Sprintf("Valid values are %s, or %s where N is a version number.",
				strings.Join(engines[:len(engines)-1], ", "), engines[len(engines)-1]),
		)
	}
	return
}

func isArgForBuild(arg string) bool {
	return !strings.HasPrefix(arg, "-") || arg == "--bundle"
}

// This returns either BuildOptions, TransformOptions, or an error
func parseOptionsForRun(osArgs []string) (*api.BuildOptions, *api.TransformOptions, parseOptionsExtras, *cli_helpers.ErrorWithNote) {
	// If there's an entry point or we're bundling, then we're building
	for _, arg := range osArgs {
		if isArgForBuild(arg) {
			options := newBuildOptions()

			// Apply defaults appropriate for the CLI
			options.LogLimit = 6
			options.LogLevel = api.LogLevelInfo
			options.Write = true

			extras, err := parseOptionsImpl(osArgs, &options, nil, kindInternal)
			if err != nil {
				return nil, nil, parseOptionsExtras{}, err
			}
			return &options, nil, extras, nil
		}
	}

	// Otherwise, we're transforming
	options := newTransformOptions()

	// Apply defaults appropriate for the CLI
	options.LogLimit = 6
	options.LogLevel = api.LogLevelInfo

	_, err := parseOptionsImpl(osArgs, nil, &options, kindInternal)
	if err != nil {
		return nil, nil, parseOptionsExtras{}, err
	}
	if options.Sourcemap != api.SourceMapNone && options.Sourcemap != api.SourceMapInline {
		var sourceMapMode string
		switch options.Sourcemap {
		case api.SourceMapExternal:
			sourceMapMode = "external"
		case api.SourceMapInlineAndExternal:
			sourceMapMode = "both"
		case api.SourceMapLinked:
			sourceMapMode = "linked"
		}
		return nil, nil, parseOptionsExtras{}, cli_helpers.MakeErrorWithNote(
			fmt.Sprintf("Use \"--sourcemap\" instead of \"--sourcemap=%s\" when transforming stdin", sourceMapMode),
			fmt.Sprintf("Using esbuild to transform stdin only generates one output file. You cannot use the %q source map mode "+
				"since that needs to generate two output files.", sourceMapMode),
		)
	}
	return nil, &options, parseOptionsExtras{}, nil
}

func splitWithEmptyCheck(s string, sep string) []string {
	// Special-case the empty string to return [] instead of [""]
	if s == "" {
		return []string{}
	}

	return strings.Split(s, sep)
}

type analyzeMode uint8

const (
	analyzeDisabled analyzeMode = iota
	analyzeEnabled
	analyzeVerbose
)

func filterAnalyzeFlags(osArgs []string) ([]string, analyzeMode) {
	for _, arg := range osArgs {
		if isArgForBuild(arg) {
			analyze := analyzeDisabled
			end := 0
			for _, arg := range osArgs {
				switch arg {
				case "--analyze":
					analyze = analyzeEnabled
				case "--analyze=verbose":
					analyze = analyzeVerbose
				default:
					osArgs[end] = arg
					end++
				}
			}
			return osArgs[:end], analyze
		}
	}
	return osArgs, analyzeDisabled
}

// Print metafile analysis after the build if it's enabled
func addAnalyzePlugin(buildOptions *api.BuildOptions, analyze analyzeMode, osArgs []string) {
	buildOptions.Plugins = append(buildOptions.Plugins, api.Plugin{
		Name: "PrintAnalysis",
		Setup: func(build api.PluginBuild) {
			color := logger.OutputOptionsForArgs(osArgs).Color
			build.OnEnd(func(result *api.BuildResult) (api.OnEndResult, error) {
				if result.Metafile != "" {
					logger.PrintTextWithColor(os.Stderr, color, func(colors logger.Colors) string {
						return api.AnalyzeMetafile(result.Metafile, api.AnalyzeMetafileOptions{
							Color:   colors != logger.Colors{},
							Verbose: analyze == analyzeVerbose,
						})
					})
					os.Stderr.WriteString("\n")
				}
				return api.OnEndResult{}, nil
			})
		},
	})

	// Always generate a metafile if we're analyzing, even if it won't be written out
	buildOptions.Metafile = true
}

func runImpl(osArgs []string, plugins []api.Plugin) int {
	// Special-case running a server
	for _, arg := range osArgs {
		if arg == "--serve" ||
			strings.HasPrefix(arg, "--serve=") ||
			strings.HasPrefix(arg, "--servedir=") ||
			strings.HasPrefix(arg, "--serve-fallback=") {
			serveImpl(osArgs)
			return 1 // There was an error starting the server if we get here
		}
	}

	osArgs, analyze := filterAnalyzeFlags(osArgs)
	buildOptions, transformOptions, extras, err := parseOptionsForRun(osArgs)

	// Add any plugins from the caller after parsing the build options
	if buildOptions != nil {
		buildOptions.Plugins = append(buildOptions.Plugins, plugins...)

		// The "--analyze" flag is implemented as a plugin
		if analyze != analyzeDisabled {
			addAnalyzePlugin(buildOptions, analyze, osArgs)
		}
	}

	switch {
	case buildOptions != nil:
		// Read the "NODE_PATH" from the environment. This is part of node's
		// module resolution algorithm. Documentation for this can be found here:
		// https://nodejs.org/api/modules.html#modules_loading_from_the_global_folders
		if value, ok := os.LookupEnv("NODE_PATH"); ok {
			separator := ":"
			if fs.CheckIfWindows() {
				// On Windows, NODE_PATH is delimited by semicolons instead of colons
				separator = ";"
			}
			buildOptions.NodePaths = splitWithEmptyCheck(value, separator)
		}

		// Read from stdin when there are no entry points
		if len(buildOptions.EntryPoints)+len(buildOptions.EntryPointsAdvanced) == 0 {
			if buildOptions.Stdin == nil {
				buildOptions.Stdin = &api.StdinOptions{}
			}
			bytes, err := ioutil.ReadAll(os.Stdin)
			if err != nil {
				logger.PrintErrorToStderr(osArgs, fmt.Sprintf(
					"Could not read from stdin: %s", err.Error()))
				return 1
			}
			buildOptions.Stdin.Contents = string(bytes)
			buildOptions.Stdin.ResolveDir, _ = os.Getwd()
		} else if buildOptions.Stdin != nil {
			if buildOptions.Stdin.Sourcefile != "" {
				logger.PrintErrorToStderr(osArgs,
					"\"sourcefile\" only applies when reading from stdin")
			} else {
				logger.PrintErrorToStderr(osArgs,
					"\"loader\" without extension only applies when reading from stdin")
			}
			return 1
		}

		// Validate the metafile absolute path and directory ahead of time so we
		// don't write any output files if it's incorrect. That makes this API
		// option consistent with how we handle all other API options.
		var writeMetafile func(string)
		if extras.metafile != nil {
			var metafileAbsPath string
			var metafileAbsDir string

			if buildOptions.Outfile == "" && buildOptions.Outdir == "" {
				// Cannot use "metafile" when writing to stdout
				logger.PrintErrorToStderr(osArgs, "Cannot use \"metafile\" without an output path")
				return 1
			}
			realFS, realFSErr := fs.RealFS(fs.RealFSOptions{AbsWorkingDir: buildOptions.AbsWorkingDir})
			if realFSErr == nil {
				absPath, ok := realFS.Abs(*extras.metafile)
				if !ok {
					logger.PrintErrorToStderr(osArgs, fmt.Sprintf("Invalid metafile path: %s", *extras.metafile))
					return 1
				}
				metafileAbsPath = absPath
				metafileAbsDir = realFS.Dir(absPath)
			} else {
				// Don't fail in this case since the error will be reported by "api.Build"
			}

			writeMetafile = func(json string) {
				if json == "" || realFSErr != nil {
					return // Don't write out the metafile on build errors
				}
				if err != nil {
					// This should already have been checked above
					panic(err.Text)
				}
				fs.BeforeFileOpen()
				defer fs.AfterFileClose()
				if err := fs.MkdirAll(realFS, metafileAbsDir, 0755); err != nil {
					logger.PrintErrorToStderr(osArgs, fmt.Sprintf(
						"Failed to create output directory: %s", err.Error()))
				} else {
					if err := ioutil.WriteFile(metafileAbsPath, []byte(json), 0666); err != nil {
						logger.PrintErrorToStderr(osArgs, fmt.Sprintf(
							"Failed to write to output file: %s", err.Error()))
					}
				}
			}
		}

		// Also validate the mangle cache absolute path and directory ahead of time
		// for the same reason
		var writeMangleCache func(map[string]interface{})
		if extras.mangleCache != nil {
			var mangleCacheAbsPath string
			var mangleCacheAbsDir string
			var mangleCacheOrder []string
			realFS, realFSErr := fs.RealFS(fs.RealFSOptions{AbsWorkingDir: buildOptions.AbsWorkingDir})
			if realFSErr == nil {
				absPath, ok := realFS.Abs(*extras.mangleCache)
				if !ok {
					logger.PrintErrorToStderr(osArgs, fmt.Sprintf("Invalid mangle cache path: %s", *extras.mangleCache))
					return 1
				}
				mangleCacheAbsPath = absPath
				mangleCacheAbsDir = realFS.Dir(absPath)
				buildOptions.MangleCache, mangleCacheOrder = parseMangleCache(osArgs, realFS, *extras.mangleCache)
				if buildOptions.MangleCache == nil {
					return 1 // Stop now if parsing failed
				}
			} else {
				// Don't fail in this case since the error will be reported by "api.Build"
			}

			writeMangleCache = func(mangleCache map[string]interface{}) {
				if mangleCache == nil || realFSErr != nil {
					return // Don't write out the metafile on build errors
				}
				if err != nil {
					// This should already have been checked above
					panic(err.Text)
				}
				fs.BeforeFileOpen()
				defer fs.AfterFileClose()
				if err := fs.MkdirAll(realFS, mangleCacheAbsDir, 0755); err != nil {
					logger.PrintErrorToStderr(osArgs, fmt.Sprintf(
						"Failed to create output directory: %s", err.Error()))
				} else {
					bytes := printMangleCache(mangleCache, mangleCacheOrder, buildOptions.Charset == api.CharsetASCII)
					if err := ioutil.WriteFile(mangleCacheAbsPath, bytes, 0666); err != nil {
						logger.PrintErrorToStderr(osArgs, fmt.Sprintf(
							"Failed to write to output file: %s", err.Error()))
					}
				}
			}
		}

		// Handle post-build actions with a plugin so they also work in watch mode
		buildOptions.Plugins = append(buildOptions.Plugins, api.Plugin{
			Name: "PostBuildActions",
			Setup: func(build api.PluginBuild) {
				build.OnEnd(func(result *api.BuildResult) (api.OnEndResult, error) {
					// Write the metafile to the file system
					if writeMetafile != nil {
						writeMetafile(result.Metafile)
					}

					// Write the mangle cache to the file system
					if writeMangleCache != nil {
						writeMangleCache(result.MangleCache)
					}

					return api.OnEndResult{}, nil
				})
			},
		})

		// Handle watch mode
		if extras.watch {
			ctx, err := api.Context(*buildOptions)

			// Only start watching if the build options passed validation
			if err != nil {
				return 1
			}

			ctx.Watch(api.WatchOptions{
				Delay: extras.watchDelay,
			})

			// Do not exit if we're in watch mode
			<-make(chan struct{})
		}

		// This prints the summary which the context API doesn't do
		result := api.Build(*buildOptions)

		// Return a non-zero exit code if there were errors
		if len(result.Errors) > 0 {
			return 1
		}

	case transformOptions != nil:
		// Read the input from stdin
		bytes, err := ioutil.ReadAll(os.Stdin)
		if err != nil {
			logger.PrintErrorToStderr(osArgs, fmt.Sprintf(
				"Could not read from stdin: %s", err.Error()))
			return 1
		}

		// Run the transform and stop if there were errors
		result := api.Transform(string(bytes), *transformOptions)
		if len(result.Errors) > 0 {
			return 1
		}

		// Write the output to stdout
		os.Stdout.Write(result.Code)

	case err != nil:
		logger.PrintErrorWithNoteToStderr(osArgs, err.Text, err.Note)
		return 1
	}

	return 0
}

func parseServeOptionsImpl(osArgs []string) (api.ServeOptions, []string, error) {
	host := ""
	portText := ""
	servedir := ""
	keyfile := ""
	certfile := ""
	fallback := ""
	var corsOrigin []string

	// Filter out server-specific flags
	filteredArgs := make([]string, 0, len(osArgs))
	for _, arg := range osArgs {
		if arg == "--serve" {
			// Just ignore this flag
		} else if strings.HasPrefix(arg, "--serve=") {
			portText = arg[len("--serve="):]
		} else if strings.HasPrefix(arg, "--servedir=") {
			servedir = arg[len("--servedir="):]
		} else if strings.HasPrefix(arg, "--keyfile=") {
			keyfile = arg[len("--keyfile="):]
		} else if strings.HasPrefix(arg, "--certfile=") {
			certfile = arg[len("--certfile="):]
		} else if strings.HasPrefix(arg, "--serve-fallback=") {
			fallback = arg[len("--serve-fallback="):]
		} else if strings.HasPrefix(arg, "--cors-origin=") {
			corsOrigin = strings.Split(arg[len("--cors-origin="):], ",")
		} else {
			filteredArgs = append(filteredArgs, arg)
		}
	}

	// Specifying the host is optional
	var err error
	if strings.ContainsRune(portText, ':') {
		host, portText, err = net.SplitHostPort(portText)
		if err != nil {
			return api.ServeOptions{}, nil, err
		}
	}

	// Parse the port
	var port int64
	if portText != "" {
		port, err = strconv.ParseInt(portText, 10, 32)
		if err != nil {
			return api.ServeOptions{}, nil, err
		}
		if port < 0 || port > 0xFFFF {
			return api.ServeOptions{}, nil, fmt.Errorf("Invalid port number: %s", portText)
		}
		if port == 0 {
			// 0 is the default value in Go, which we interpret as "try to
			// pick port 8000". So Go uses -1 as the sentinel value instead.
			port = -1
		}
	}

	return api.ServeOptions{
		Port:     int(port),
		Host:     host,
		Servedir: servedir,
		Keyfile:  keyfile,
		Certfile: certfile,
		Fallback: fallback,
		CORS: api.CORSOptions{
			Origin: corsOrigin,
		},
	}, filteredArgs, nil
}

func serveImpl(osArgs []string) {
	serveOptions, filteredArgs, err := parseServeOptionsImpl(osArgs)
	if err != nil {
		logger.PrintErrorWithNoteToStderr(osArgs, err.Error(), "")
		return
	}

	options := newBuildOptions()

	// Apply defaults appropriate for the CLI
	options.LogLimit = 5
	options.LogLevel = api.LogLevelInfo

	filteredArgs, analyze := filterAnalyzeFlags(filteredArgs)
	extras, errWithNote := parseOptionsImpl(filteredArgs, &options, nil, kindInternal)
	if errWithNote != nil {
		logger.PrintErrorWithNoteToStderr(osArgs, errWithNote.Text, errWithNote.Note)
		return
	}
	if analyze != analyzeDisabled {
		addAnalyzePlugin(&options, analyze, osArgs)
	}

	serveOptions.OnRequest = func(args api.ServeOnRequestArgs) {
		logger.PrintText(os.Stderr, logger.LevelInfo, filteredArgs, func(colors logger.Colors) string {
			statusColor := colors.Red
			if args.Status >= 200 && args.Status <= 299 {
				statusColor = colors.Green
			} else if args.Status >= 300 && args.Status <= 399 {
				statusColor = colors.Yellow
			}
			return fmt.Sprintf("%s%s - %q %s%d%s [%dms]%s\n",
				colors.Dim, args.RemoteAddress, args.Method+" "+args.Path,
				statusColor, args.Status, colors.Dim, args.TimeInMS, colors.Reset)
		})
	}

	// Validate build options
	ctx, ctxErr := api.Context(options)
	if ctxErr != nil {
		return
	}

	// Try to enable serve mode
	if _, err = ctx.Serve(serveOptions); err != nil {
		logger.PrintErrorWithNoteToStderr(osArgs, err.Error(), "")
		return
	}

	// Also enable watch mode if it was requested
	if extras.watch {
		if err := ctx.Watch(api.WatchOptions{}); err != nil {
			logger.PrintErrorWithNoteToStderr(osArgs, err.Error(), "")
			return
		}
	}

	// Do not exit if we're in serve mode
	<-make(chan struct{})
}

func parseLogLevel(value string, arg string) (api.LogLevel, *cli_helpers.ErrorWithNote) {
	switch value {
	case "verbose":
		return api.LogLevelVerbose, nil
	case "debug":
		return api.LogLevelDebug, nil
	case "info":
		return api.LogLevelInfo, nil
	case "warning":
		return api.LogLevelWarning, nil
	case "error":
		return api.LogLevelError, nil
	case "silent":
		return api.LogLevelSilent, nil
	default:
		return api.LogLevelSilent, cli_helpers.MakeErrorWithNote(
			fmt.Sprintf("Invalid value %q in %q", value, arg),
			"Valid values are \"verbose\", \"debug\", \"info\", \"warning\", \"error\", or \"silent\".",
		)
	}
}
