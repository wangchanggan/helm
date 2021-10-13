# Helm源码分析
Source Code From https://github.com/helm/helm/archive/refs/tags/v2.17.0.tar.gz

参考云原生应用管理原理与实践（陈显鹭 阚俊宝 匡大虎 卢稼奇 著）

## 目录
-   [Helm源码分析](#helm源码分析)
    -   [目录](#目录)
    -   [helm install](#helm-install)
        -   [locateChartPath](#locatechartpath)
        -   [ensureHelmClient](#ensurehelmclient)

## helm install
cmd\helm\install.go:172

### locateChartPath
cmd\helm\install.go:476

pkg\repo\chartrepo.go:206

![image](flow_charts/Chart流程下载.png)

### ensureHelmClient
cmd\helm\helm.go:413, 422