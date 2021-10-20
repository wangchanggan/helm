# Helm源码分析
Source Code From https://github.com/helm/helm/archive/refs/tags/v2.17.0.tar.gz

参考云原生应用管理原理与实践（陈显鹭 阚俊宝 匡大虎 卢稼奇 著）

## 目录
-   [Helm源码分析](#helm源码分析)
    -   [目录](#目录)
    -   [Helm Install Client](#helm-install-client)
        -   [locateChartPath](#locatechartpath)
        -   [ensureHelmClient](#ensurehelmclient)
        -   [InstallCmd Run](#installcmd-run)
        -   [installReleaseFromChart](#installreleasefromchart)
        -   [setupConnection](#setupconnection)
        -   [Helm Client install
            Function](#helm-client-install-function)
        -   [返回Release状态](#返回release状态)
    -   [Helm Install Server](#helm-install-server)
        -   [prepareRelease](#preparerelease)
        -   [performRelease](#performrelease)
    -   [Helm Update](#helm-update)
        -   [update命令的定义](#update命令的定义)
        -   [Update服务端的实现](#update服务端的实现)
    -   [Helm Ls](#helm-ls)
        -   [Client端实现](#client端实现)
        -   [Server端实现](#server端实现)
    -   [Helm Rollback](#helm-rollback)

## Helm Install Client
cmd/helm/install.go:172

![image](flow_charts/客户端helm安装.png)

1.提前使用kubectl port-forward功能打通宿主机与远程Tiller Pod的通信。

2.checkArgsLength检查用户输入参数的合法性。

3.对于远程地址，locateChartPath下载Chart到本地指定目录；对于本地地址，则直接加载。

4.installCmd.run将用户命令行输入参数覆盖values.yaml信息，下载依赖的Chart，将Chart信息加载到内存中变成结构体信息。

5.向Tiller发送install命令，将含有Chart所有信息的结构体发送出去。

6.打印Tiller返回的Release信息。

7.向Tiller发送获取Release Status信息并且打印出来。

### locateChartPath
cmd/helm/install.go:491

pkg/repo/chartrepo.go:206

![image](flow_charts/Chart流程下载.png)

### ensureHelmClient
cmd/helm/helm.go:421, 430

### InstallCmd Run

cmd/helm/install.go:242

1.处理helm install命令行临时覆盖的参数

2.处理外部介入的template

3.检查指定的Release名称是否符合DNS命名规范

4.加载Chart文件并且初始化为Chart对象

5.加载requirements.yaml文件内声明的依赖Chart

### installReleaseFromChart
pkg/helm/client.go:112

### setupConnection
cmd/helm/install.go:177

cmd/helm/helm.go:341

### Helm Client install Function
pkg/helm/client.go:422,361

pkg/proto/hapi/services/tiller.pb.go:1566

### 返回Release状态
cmd/helm/install.go:357

## Helm Install Server
pkg/tiller/release_install.go:34

### prepareRelease
pkg/tiller/release_install.go:61

1.首先检查当前Release名称是否是集群唯一，这是Release唯一性的标志，如果没有指定Release名称，那么就创建一个全集群唯一的名称。

2.检查客户端、服务端、Kubernetes Api Server的版本兼容性。

3.初始化ReleaseOptions结构体，填入名称、版本号、命名空间等信息。

4.ToRenderValuesCaps将手动传递的参数和默认已经存在的values渲染到一起。

5.renderResources将上一步构建的信息和Kubernetes Api Ssever等信息合并在一起，同时分离出安装Chart Yaml信息和hooks等。

6.拼接出Release对象，真正开始进入安装步骤。

### performRelease
pkg/tiller/release_install.go:158

pkg/tiller/release_server.go:433,423

pkg/tiller/release_modules.go:53

1.将Hooks按照权重的顺序依次排序，优先级从高到低，一般需要先安装pre-istall

2.deleteHookByPolicy丽数将需要在安装前删除的资源优先删除。

3.将剩余需要创建的资源，尤其是CRD资源提交给集群创建。

4.提交后一直等待资源创建完毕，默认超时时间为1min。

5.全部Hooks资源创建完毕后，代表该Chart Hooks准备完毕。

6.将Release信息记录到Kubernetes集群中，目前Helm默认的方式是记录到kube-system下的configmap中。configmap的名称就是Release的名字，后面的点对应着它的版本号。比如v1就是第一次安装的版本，后面的labels也能表明些身份，比如OWNER=TILLER，代表它是Tiller创建的。

7.基本信息记录完毕后，将剩余Chart谊染后的资源文件提交给Kubernetes ApiServer。

## Helm Update
### update命令的定义
cmd\helm\upgrade.go:131

1.首先创建与Tiller的联通通道。

2.检查传入参数的合法性。

3.下载对应的Chart包。

4.将对应的传入setting值构建成一个Release结构体。

5.调用 Tiller update接口。

6.检查Release状态。

### Update服务端的实现
pkg\tiller\release_update.go:34,276

## Helm Ls
### Client端实现
cmd\helm\list.go:149

pkg\helm\client.go:390

### Server端实现
pkg\tiller\release_list.go:30

pkg\storage\driver\cfgmaps.go:90

## Helm Rollback
pkg\tiller\release_rollback.go:64

pkg\tiller\release_rollback.go:123

1.检查传递的Release名称是否符合规则。

2.根据传递的需要，回滚版本号来确定目标版本。

3.根据版本号去configmap中读取对应的版本号信息。

4.将读取到的版本号信息格式化成对应的Release结构体。

5.拼装成结构体后返回。
