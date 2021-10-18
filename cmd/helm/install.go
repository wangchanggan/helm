/*
Copyright The Helm Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig"
	"github.com/ghodss/yaml"
	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/helm/pkg/chartutil"
	"k8s.io/helm/pkg/downloader"
	"k8s.io/helm/pkg/getter"
	"k8s.io/helm/pkg/helm"
	"k8s.io/helm/pkg/kube"
	"k8s.io/helm/pkg/proto/hapi/release"
	"k8s.io/helm/pkg/renderutil"
	"k8s.io/helm/pkg/repo"
	"k8s.io/helm/pkg/strvals"
)

const installDesc = `
This command installs a chart archive.

The install argument must be a chart reference, a path to a packaged chart,
a path to an unpacked chart directory or a URL.

To override values in a chart, use either the '--values' flag and pass in a file
or use the '--set' flag and pass configuration from the command line.  To force string
values in '--set', use '--set-string' instead. In case a value is large and therefore
you want not to use neither '--values' nor '--set', use '--set-file' to read the
single large value from file.

	$ helm install -f myvalues.yaml ./redis

or

	$ helm install --set name=prod ./redis

or

	$ helm install --set-string long_int=1234567890 ./redis

or
    $ helm install --set-file multiline_text=path/to/textfile

You can specify the '--values'/'-f' flag multiple times. The priority will be given to the
last (right-most) file specified. For example, if both myvalues.yaml and override.yaml
contained a key called 'Test', the value set in override.yaml would take precedence:

	$ helm install -f myvalues.yaml -f override.yaml ./redis

You can specify the '--set' flag multiple times. The priority will be given to the
last (right-most) set specified. For example, if both 'bar' and 'newbar' values are
set for a key called 'foo', the 'newbar' value would take precedence:

	$ helm install --set foo=bar --set foo=newbar ./redis


To check the generated manifests of a release without installing the chart,
the '--debug' and '--dry-run' flags can be combined. This will still require a
round-trip to the Tiller server.

If --verify is set, the chart MUST have a provenance file, and the provenance
file MUST pass all verification steps.

There are five different ways you can express the chart you want to install:

1. By chart reference: helm install stable/mariadb
2. By path to a packaged chart: helm install ./nginx-1.2.3.tgz
3. By path to an unpacked chart directory: helm install ./nginx
4. By absolute URL: helm install https://example.com/charts/nginx-1.2.3.tgz
5. By chart reference and repo url: helm install --repo https://example.com/charts/ nginx

CHART REFERENCES

A chart reference is a convenient way of reference a chart in a chart repository.

When you use a chart reference with a repo prefix ('stable/mariadb'), Helm will look in the local
configuration for a chart repository named 'stable', and will then look for a
chart in that repository whose name is 'mariadb'. It will install the latest
version of that chart unless you also supply a version number with the
'--version' flag.

To see the list of chart repositories, use 'helm repo list'. To search for
charts in a repository, use 'helm search'.
`

type installCmd struct {
	name           string
	namespace      string
	valueFiles     valueFiles
	chartPath      string
	dryRun         bool
	disableHooks   bool
	disableCRDHook bool
	replace        bool
	verify         bool
	keyring        string
	out            io.Writer
	client         helm.Interface
	values         []string
	stringValues   []string
	fileValues     []string
	nameTemplate   string
	version        string
	timeout        int64
	wait           bool
	atomic         bool
	repoURL        string
	username       string
	password       string
	devel          bool
	depUp          bool
	subNotes       bool
	description    string

	certFile string
	keyFile  string
	caFile   string
	output   string
}

type valueFiles []string

func (v *valueFiles) String() string {
	return fmt.Sprint(*v)
}

func (v *valueFiles) Type() string {
	return "valueFiles"
}

func (v *valueFiles) Set(value string) error {
	for _, filePath := range strings.Split(value, ",") {
		*v = append(*v, filePath)
	}
	return nil
}

func newInstallCmd(c helm.Interface, out io.Writer) *cobra.Command {
	inst := &installCmd{
		out:    out,
		client: c,
	}

	cmd := &cobra.Command{
		Use:   "install [CHART]",
		Short: "Install a chart archive",
		Long:  installDesc,
		// 在install命令行时，Helm 还执行了一个操作，使用kubectl port-forward临时在本地宿主机打通一个与Tiller Pod沟通的通道
		PreRunE: func(_ *cobra.Command, _ []string) error { return setupConnection() },
		RunE: func(cmd *cobra.Command, args []string) error {
			// 检查传递参数的有效性，以及是否漏传参数。
			if err := checkArgsLength(len(args), "chart name"); err != nil {
				return err
			}

			debug("Original chart version: %q", inst.version)
			if inst.version == "" && inst.devel {
				debug("setting version to >0.0.0-0")
				inst.version = ">0.0.0-0"
			}

			// 寻找Chart位置，如果是本地目录，则返回并寻找完全路径；如果URL，则下载到指定路径后返回该路径名称。
			cp, err := locateChartPath(inst.repoURL, inst.username, inst.password, args[0], inst.version, inst.verify, inst.keyring,
				inst.certFile, inst.keyFile, inst.caFile)
			if err != nil {
				return err
			}
			inst.chartPath = cp
			// 初始化Helm Client，用来与 Tiller 通信。
			inst.client = ensureHelmClient(inst.client)
			inst.wait = inst.wait || inst.atomic
			// 真正的业务逻辑，分别检查Chart依赖等信息，然后给Tiller发送解压后的模板信息。
			return inst.run()
		},
	}

	f := cmd.Flags()
	settings.AddFlagsTLS(f)
	f.VarP(&inst.valueFiles, "values", "f", "Specify values in a YAML file or a URL(can specify multiple)")
	f.StringVarP(&inst.name, "name", "n", "", "The release name. If unspecified, it will autogenerate one for you")
	f.StringVar(&inst.namespace, "namespace", "", "Namespace to install the release into. Defaults to the current kube config namespace.")
	f.BoolVar(&inst.dryRun, "dry-run", false, "Simulate an install")
	f.BoolVar(&inst.disableHooks, "no-hooks", false, "Prevent hooks from running during install")
	f.BoolVar(&inst.disableCRDHook, "no-crd-hook", false, "Prevent CRD hooks from running, but run other hooks")
	f.BoolVar(&inst.replace, "replace", false, "Re-use the given name, even if that name is already used. This is unsafe in production")
	f.StringArrayVar(&inst.values, "set", []string{}, "Set values on the command line (can specify multiple or separate values with commas: key1=val1,key2=val2)")
	f.StringArrayVar(&inst.stringValues, "set-string", []string{}, "Set STRING values on the command line (can specify multiple or separate values with commas: key1=val1,key2=val2)")
	f.StringArrayVar(&inst.fileValues, "set-file", []string{}, "Set values from respective files specified via the command line (can specify multiple or separate values with commas: key1=path1,key2=path2)")
	f.StringVar(&inst.nameTemplate, "name-template", "", "Specify template used to name the release")
	f.BoolVar(&inst.verify, "verify", false, "Verify the package before installing it")
	f.StringVar(&inst.keyring, "keyring", defaultKeyring(), "Location of public keys used for verification")
	f.StringVar(&inst.version, "version", "", "Specify the exact chart version to install. If this is not specified, the latest version is installed")
	f.Int64Var(&inst.timeout, "timeout", 300, "Time in seconds to wait for any individual Kubernetes operation (like Jobs for hooks)")
	f.BoolVar(&inst.wait, "wait", false, "If set, will wait until all Pods, PVCs, Services, and minimum number of Pods of a Deployment are in a ready state before marking the release as successful. It will wait for as long as --timeout")
	f.BoolVar(&inst.atomic, "atomic", false, "If set, installation process purges chart on fail, also sets --wait flag")
	f.StringVar(&inst.repoURL, "repo", "", "Chart repository url where to locate the requested chart")
	f.StringVar(&inst.username, "username", "", "Chart repository username where to locate the requested chart")
	f.StringVar(&inst.password, "password", "", "Chart repository password where to locate the requested chart")
	f.StringVar(&inst.certFile, "cert-file", "", "Identify HTTPS client using this SSL certificate file")
	f.StringVar(&inst.keyFile, "key-file", "", "Identify HTTPS client using this SSL key file")
	f.StringVar(&inst.caFile, "ca-file", "", "Verify certificates of HTTPS-enabled servers using this CA bundle")
	f.BoolVar(&inst.devel, "devel", false, "Use development versions, too. Equivalent to version '>0.0.0-0'. If --version is set, this is ignored.")
	f.BoolVar(&inst.depUp, "dep-up", false, "Run helm dependency update before installing the chart")
	f.BoolVar(&inst.subNotes, "render-subchart-notes", false, "Render subchart notes along with the parent")
	f.StringVar(&inst.description, "description", "", "Specify a description for the release")
	bindOutputFlag(cmd, &inst.output)

	// set defaults from environment
	settings.InitTLS(f)

	return cmd
}

func (i *installCmd) run() error {
	debug("CHART PATH: %s\n", i.chartPath)

	// 查看是否填写了命名容间名称，如果没有填写则默认是default命名空间。
	if i.namespace == "" {
		i.namespace = defaultNamespace()
	}

	// 使用vals函数合并命令行"helm install -f myvalues.yam1"覆盖的参数，这里使用的"-f override.yaml"这种命令行传参方式就是通过vals函数实现的。
	// 此函数将valueFiles,values等信息读取出来后，和已经加裁到内存的valuesMap做一个对比，用外部传入的参数覆盖当前内存中的参数
	// 这样在继续进行后面的动作时，都是使用的外部命令行的最新参数列表。
	rawVals, err := vals(i.valueFiles, i.values, i.stringValues, i.fileValues, i.certFile, i.keyFile, i.caFile)
	if err != nil {
		return err
	}

	// If template is specified, try to run the template.
	// 如果在执行install命令时指定了template，这里就会根据template的名称使用go template模板库进行读取
	// 同时也会自动渲染该模板，最终返回一个被渲染过的template对象。
	if i.nameTemplate != "" {
		i.name, err = generateName(i.nameTemplate)
		if err != nil {
			return err
		}
		// Print the final name so the user knows what the final name of the release is.
		fmt.Printf("FINAL NAME: %s\n", i.name)
	}

	// 这检查指定的名称是否符合DNS命名规范，这个规范适用于Kubernetes各个资源的命名，算是Kubernetes各个资源部署的统一标准。
	if msgs := validation.IsDNS1123Subdomain(i.name); i.name != "" && len(msgs) > 0 {
		return fmt.Errorf("release name %s is invalid: %s", i.name, strings.Join(msgs, ";"))
	}

	// Check chart requirements to make sure all dependencies are present in /charts
	// 根据前面返回的Chart本地存储路径加载对应的Chart文件。这里的Chart文件一般都是一个文件夹，里面含有values.yaml、Chart等各种文件和文件夹
	// 该函数读取这些文件的内容后，将其初始化为一个对应的Chart，这样既能校验Chart内容的正确性也方便后面继续调用。
	// 如果设置了.helmignore 文件，那么这个函数也会略过这些文件，不会将其序列化到Chart对象中。
	chartRequested, err := chartutil.Load(i.chartPath)
	if err != nil {
		return prettyError(err)
	}

	if chartRequested.Metadata.Deprecated {
		fmt.Fprintln(os.Stderr, "WARNING: This chart is deprecated")
	}

	// 检查是否有requirements.yaml 文件，并且将声明的文件内容使用上面介绍的Chart重新下载和读取渲染函数来重新运行一遍。
	// 全部下载完毕后，还会再使用Load函数加载内容加载。到此，内存中的Chart结构体已经包含所有需要的文本信息。
	if req, err := chartutil.LoadRequirements(chartRequested); err == nil {
		// If checkDependencies returns an error, we have unfulfilled dependencies.
		// As of Helm 2.4.0, this is treated as a stopping condition:
		// https://github.com/kubernetes/helm/issues/2209
		if err := renderutil.CheckDependencies(chartRequested, req); err != nil {
			if i.depUp {
				man := &downloader.Manager{
					Out:        i.out,
					ChartPath:  i.chartPath,
					HelmHome:   settings.Home,
					Keyring:    defaultKeyring(),
					SkipUpdate: false,
					Getters:    getter.All(settings),
				}
				if err := man.Update(); err != nil {
					return prettyError(err)
				}

				// Update all dependencies which are present in /charts.
				chartRequested, err = chartutil.Load(i.chartPath)
				if err != nil {
					return prettyError(err)
				}
			} else {
				return prettyError(err)
			}

		}
	} else if err != chartutil.ErrRequirementsNotFound {
		return fmt.Errorf("cannot load requirements: %v", err)
	}

	res, err := i.client.InstallReleaseFromChart(
		chartRequested,
		i.namespace,
		helm.ValueOverrides(rawVals),
		helm.ReleaseName(i.name),
		helm.InstallDryRun(i.dryRun),
		helm.InstallReuseName(i.replace),
		helm.InstallDisableHooks(i.disableHooks),
		helm.InstallDisableCRDHook(i.disableCRDHook),
		helm.InstallSubNotes(i.subNotes),
		helm.InstallTimeout(i.timeout),
		helm.InstallWait(i.wait),
		helm.InstallDescription(i.description))
	if err != nil {
		if i.atomic {
			fmt.Fprintf(os.Stdout, "INSTALL FAILED\nPURGING CHART\nError: %v\n", prettyError(err))
			deleteSideEffects := &deleteCmd{
				name:         i.name,
				disableHooks: i.disableHooks,
				purge:        true,
				timeout:      i.timeout,
				description:  "",
				dryRun:       i.dryRun,
				out:          i.out,
				client:       i.client,
			}
			if err := deleteSideEffects.run(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "Successfully purged a chart!\n")
		}
		return prettyError(err)
	}

	// 首先获取Release信息，在上步发送install请求后，Tiller的response信息内就已经含有了这些信息。
	rel := res.GetRelease()
	if rel == nil {
		return nil
	}

	if outputFormat(i.output) == outputTable {
		i.printRelease(rel)
	}

	// If this is a dry run, we can't display status.
	if i.dryRun {
		// This is special casing to avoid breaking backward compatibility:
		if res.Release.Info.Description != "Dry run complete" {
			fmt.Fprintf(os.Stdout, "WARNING: %s\n", res.Release.Info.Description)
		}
		return nil
	}

	// Print the status like status command does
	// 和install一样， 也是向Tiller发送请求，这个API的URL /hapi.services.tiller.ReleaseService/GetReleaseStatus
	status, err := i.client.ReleaseStatus(rel.Name)
	if err != nil {
		return prettyError(err)
	}

	return write(i.out, &statusWriter{status}, outputFormat(i.output))
}

// Merges source and destination map, preferring values from the source map
func mergeValues(dest map[string]interface{}, src map[string]interface{}) map[string]interface{} {
	for k, v := range src {
		// If the key doesn't exist already, then just set the key to that value
		if _, exists := dest[k]; !exists {
			dest[k] = v
			continue
		}
		nextMap, ok := v.(map[string]interface{})
		// If it isn't another map, overwrite the value
		if !ok {
			dest[k] = v
			continue
		}
		// Edge case: If the key exists in the destination, but isn't a map
		destMap, isMap := dest[k].(map[string]interface{})
		// If the source map has a map for this key, prefer it
		if !isMap {
			dest[k] = v
			continue
		}
		// If we got to this point, it is a map in both, so merge them
		dest[k] = mergeValues(destMap, nextMap)
	}
	return dest
}

// vals merges values from files specified via -f/--values and
// directly via --set or --set-string or --set-file, marshaling them to YAML
func vals(valueFiles valueFiles, values []string, stringValues []string, fileValues []string, CertFile, KeyFile, CAFile string) ([]byte, error) {
	base := map[string]interface{}{}

	// User specified a values files via -f/--values
	for _, filePath := range valueFiles {
		currentMap := map[string]interface{}{}

		var bytes []byte
		var err error
		if strings.TrimSpace(filePath) == "-" {
			bytes, err = ioutil.ReadAll(os.Stdin)
		} else {
			bytes, err = readFile(filePath, CertFile, KeyFile, CAFile)
		}

		if err != nil {
			return []byte{}, err
		}

		if err := yaml.Unmarshal(bytes, &currentMap); err != nil {
			return []byte{}, fmt.Errorf("failed to parse %s: %s", filePath, err)
		}
		// Merge with the previous map
		base = mergeValues(base, currentMap)
	}

	// User specified a value via --set
	for _, value := range values {
		if err := strvals.ParseInto(value, base); err != nil {
			return []byte{}, fmt.Errorf("failed parsing --set data: %s", err)
		}
	}

	// User specified a value via --set-string
	for _, value := range stringValues {
		if err := strvals.ParseIntoString(value, base); err != nil {
			return []byte{}, fmt.Errorf("failed parsing --set-string data: %s", err)
		}
	}

	// User specified a value via --set-file
	for _, value := range fileValues {
		reader := func(rs []rune) (interface{}, error) {
			bytes, err := readFile(string(rs), CertFile, KeyFile, CAFile)
			return string(bytes), err
		}
		if err := strvals.ParseIntoFile(value, base, reader); err != nil {
			return []byte{}, fmt.Errorf("failed parsing --set-file data: %s", err)
		}
	}

	return yaml.Marshal(base)
}

// printRelease prints info about a release if the Debug is true.
func (i *installCmd) printRelease(rel *release.Release) {
	if rel == nil {
		return
	}
	// TODO: Switch to text/template like everything else.
	fmt.Fprintf(i.out, "NAME:   %s\n", rel.Name)
	if settings.Debug {
		printRelease(i.out, rel)
	}
}

// locateChartPath looks for a chart directory in known places, and returns either the full path or an error.
//
// This does not ensure that the chart is well-formed; only that the requested filename exists.
//
// Order of resolution:
// - current working directory
// - if path is absolute or begins with '.', error out here
// - chart repos in $HELM_HOME
// - URL
//
// If 'verify' is true, this will attempt to also verify the chart.
func locateChartPath(repoURL, username, password, name, version string, verify bool, keyring,
	certFile, keyFile, caFile string) (string, error) {
	// 第一优先级是当前目录，对传入的目录去掉左右空行后，通过库函数判断当前文件夹是否存在，如果存在，则返回当前文件夹的全局绝对路径
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if fi, err := os.Stat(name); err == nil {
		abs, err := filepath.Abs(name)
		if err != nil {
			return abs, err
		}
		if verify {
			if fi.IsDir() {
				return "", errors.New("cannot verify a directory")
			}
			if _, err := downloader.VerifyChart(abs, keyring); err != nil {
				return "", err
			}
		}
		return abs, nil
	}
	// 如果传入的是绝对路径或者是以 . 开头的路径，则直接返回失败
	if filepath.IsAbs(name) || strings.HasPrefix(name, ".") {
		return name, fmt.Errorf("path %q not found", name)
	}

	// 如果前面两种都不是，则从 HELM_HOME 中寻找对应的文件，并返回对应的绝对路径
	crepo := filepath.Join(settings.Home.Repository(), name)
	if _, err := os.Stat(crepo); err == nil {
		return filepath.Abs(crepo)
	}

	// 除了如上3种情况外，下面提供一个URL，需要从URL下载
	dl := downloader.ChartDownloader{
		HelmHome: settings.Home,
		Out:      os.Stdout,
		Keyring:  keyring,
		Getters:  getter.All(settings),
		Username: username,
		Password: password,
	}
	if verify {
		dl.Verify = downloader.VerifyAlways
	}
	// 如果设置了Chart repo，就会从对应的chart repo处查找Chart、
	if repoURL != "" {
		chartURL, err := repo.FindChartInAuthRepoURL(repoURL, username, password, name, version,
			certFile, keyFile, caFile, getter.All(settings))
		if err != nil {
			return "", err
		}
		name = chartURL
	}

	// 如果默认设置的下载路径没有对应的文件夹，那么先创建对应的文件夹。
	if _, err := os.Stat(settings.Home.Archive()); os.IsNotExist(err) {
		os.MkdirAll(settings.Home.Archive(), 0744)
	}

	// 将前面拼接好的带有URL路径的文件，或者用户直接提供的全路径http文件下载到对应的文件夹中，最后返回这个下载到本地路径的文件。
	filename, _, err := dl.DownloadTo(name, version, settings.Home.Archive())
	if err == nil {
		lname, err := filepath.Abs(filename)
		if err != nil {
			return filename, err
		}
		debug("Fetched %s to %s\n", name, filename)
		return lname, nil
	} else if settings.Debug {
		return filename, err
	}

	return filename, fmt.Errorf("failed to download %q (hint: running `helm repo update` may help)", name)
}

func generateName(nameTemplate string) (string, error) {
	t, err := template.New("name-template").Funcs(sprig.TxtFuncMap()).Parse(nameTemplate)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	err = t.Execute(&b, nil)
	if err != nil {
		return "", err
	}
	return b.String(), nil
}

func defaultNamespace() string {
	if ns, _, err := kube.GetConfig(settings.KubeContext, settings.KubeConfig).Namespace(); err == nil {
		return ns
	}
	return "default"
}

//readFile load a file from the local directory or a remote file with a url.
func readFile(filePath, CertFile, KeyFile, CAFile string) ([]byte, error) {
	u, _ := url.Parse(filePath)
	p := getter.All(settings)

	// FIXME: maybe someone handle other protocols like ftp.
	getterConstructor, err := p.ByScheme(u.Scheme)

	if err != nil {
		return ioutil.ReadFile(filePath)
	}

	getter, err := getterConstructor(filePath, CertFile, KeyFile, CAFile)
	if err != nil {
		return []byte{}, err
	}
	data, err := getter.Get(filePath)
	return data.Bytes(), err
}
