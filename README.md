# Cisco pxGrid Cloud SDK for Go

![Tests](https://github.com/cisco-pxgrid/cloud-sdk-go/actions/workflows/test.yaml/badge.svg?branch=main) ![Static Analysis](https://github.com/cisco-pxgrid/cloud-sdk-go/actions/workflows/lint.yaml/badge.svg?branch=main) [![Release](https://img.shields.io/github/v/release/cisco-pxgrid/cloud-sdk-go?sort=semver)](https://github.com/cisco-pxgrid/cloud-sdk-go/releases/latest) [![Docs](https://pkg.go.dev/badge/github.com/cisco-pxgrid/cloud-sdk-go)](https://pkg.go.dev/github.com/cisco-pxgrid/cloud-sdk-go)

# Overview

`cloud-sdk-go` is the Cisco pxGrid Cloud SDK for the Go programming language. This SDK requires a
minimum version of `Go 1.18`.

The SDK lets you easily create applications for Cisco pxGrid Cloud and consume pxGrid, ERS and
OpenAPI services from Cisco Identity Services Engine (ISE) devices.

## Features

- Multi-tenancy support
- Create an instance of an application
- Link application with a Cisco DNA - Cloud tenant
- Receive pxGrid topic messages from Cisco ISE devices
- Invoke HTTP APIs on Cisco ISE devices
    + pxGrid APIs
    + ERS APIs
    + OpenAPI APIs

# Install

```sh
go get github.com/cisco-pxgrid/cloud-sdk-go
```

# Usage

## Examples

### Create a new app instance

```go
import (
    "fmt"
    sdk "github.com/cisco-pxgrid/cloud-sdk-go"
)

// activationHandler is invoked when the app gets activated for a new device for a tenant
func activationHandler(device *sdk.Device) {
    fmt.Printf("New device activated: %s\n", device)
}

// deactivationHandler is invoked when the app gets deactivated for an existing device for a tenant
func deactivationHandler(device *sdk.Device) {
    fmt.Printf("Device deactivated: %s\n", device)
}

// messageHandler is invoked when there's a new message received by the app for a device
func messageHandler(id string, device *sdk.Device, stream string, payload []byte) {
    fmt.Printf("Received new message (%s) from %s\n", id, device)
    fmt.Printf("Message stream: %s\n", stream)
    fmt.Printf("Message payload: %s\n", payload)
}

// getCredentials is invoked whenever the app needs to retrieve the app credentials
func getCredentials() (*sdk.Credentials, error) {
    return &sdk.Credentials{
        ApiKey: []byte("api-key-obtained-during-app-onboarding"),
    }, nil
}

func main() {
    // create app configuration required for creating a new app instance
    config := sdk.Config{
        ID:                        "pxgrid-cloud-sample-app",
        GetCredentials:            getCredentials,
        RegionalFQDN:              "regional.cloud.cisco.com",
        GlobalFQDN:                "global.cloud.cisco.com",
        DeviceActivationHandler:   activationHandler,
        DeviceDeactivationHandler: deactivationHandler,
        DeviceMessageHandler:      messageHandler,
        ReadStreamID:              "app--pxgrid-cloud-sample-app-R",
        WriteStreamID:             "app--pxgrid-cloud-sample-app-W",
    }

   	app, err := sdk.New(config)
    if err != nil {
        fmt.Printf("Failed to create new app: %v", err)
        os.Exit(1)
    }
    // make sure to Close() the app instance to release resources acquired during app creation
    defer func() {
        if err := app.Close(); err != nil {
            fmt.Printf("Disconnected with error: %v", err)
        }
    }()

    // wait for any errors from the app instance created above
    err = <- app.Error
    if err != nil {
        fmt.Printf("%s received an error: %v", app, err)
    }
}
```

### Link and unlink with a tenant

```go
tenant, err := app.LinkTenant("otp-obtained-from-cisco-dna-portal")
if err != nil {
    fmt.Printf("Failed to obtain tenant information using supplied OTP: %v", err)
}

// Securely store tenant.ID(), tenant.Name() and tenant.ApiToken()

err = app.UnlinkTenant(tenant)
if err != nil {
    fmt.Printf("Failed to unlink %s from %s: %v", tenant, app, err)
}
```

### Setup already linked tenants

If the app needs to be restarted, use the stored tenant info to re-link.

```go
tenant, err := app.SetTenant("tenant-id", "tenant-name", "tenant-api-token")
if err != nil {
    fmt.Printf("Failed to set tenant: %v", err)
}
```

### Create app instances

If the app is registered as multi-instance, create and delete app instances can be used.

```go
// With the parent app object, create an app instance
appInstance, err = app.CreateAppInstance(ac.Name)

// Securely store appInstance.ID(), appInstance.ApiKey()

// appInstance can then be used to LinkTenant
tenant, err = appInstance.LinkTenant("otp-obtained-from-cisco-dna-portal")

// When finish, delete app instance with parent app
app.DeleteAppInstance(appInstance.ID())

```


### Invoke an ERS or OpenAPI HTTP API call on a device

ERS and OpenAPI URLs start with "/ers" and "/api" respectively.

[API guide and reference](https://developer.cisco.com/docs/identity-services-engine/latest/#!cisco-ise-api-framework)

```go
req, _ := http.NewRequest(http.MethodGet, "/ers/config/op/systemconfig/iseversion", nil)
resp, err := device.Query(req)
if err != nil {
    fmt.Printf("Failed to invoke %s on %s: %v", req, device, err)
}
```

### Invoke pxGrid HTTP API on a device
[pxGrid Reference](https://github.com/cisco-pxgrid/pxgrid-rest-ws/wiki)

In order to invoke pxGrid APIs directly on the device, ServiceLookup is not required.
Instead map the service name to the keyword and append it to the URL along with pxgrid

| pxGrid Service                   | pxGrid Cloud Service |
|----------------------------------|----------------------|
| com.cisco.ise.session            | session              |
| com.cisco.ise.config.anc         | anc                  |
| com.cisco.ise.mdm                | mdm                  |
| com.cisco.ise.config.profiler    | profiler             |
| com.cisco.ise.radius             | radius               |
| com.cisco.ise.trustsec           | trustsec             |
| com.cisco.ise.config.trustsec    | trustsec             |
| com.cisco.ise.sxp                | trustsec             |
| com.cisco.ise.echo               | echo                 |


```go
req, _ := http.NewRequest(http.MethodPost, "/pxgrid/trustsec/getSecurityGroups", strings.NewReader("{}"))
resp, err := device.Query(req)
if err != nil {
    fmt.Printf("Failed to invoke %s on %s: %v", req, device, err)
}
```

### Query limitation
There is currently a limitation on the payload size

| Size  | From ISE versions |
|-------|-------------------|
| 500KB | 3.1p3, 3.2        |
| 50MB  | 3.2p2, 3.3        |
| 2GB   | 3.2p3, 3.3p1, 3.4 |


### Stream names mappings

These are the mappings of pxGrid Cloud stream names from pxGrid topic

| pxGrid Service                   | pxGrid property name  | pxGrid Cloud stream name            |
|----------------------------------|-----------------------|-------------------------------------|
| com.cisco.ise.session            | sessionTopic          | pxcloud--session-sessions           |
| com.cisco.ise.session            | groupTopic            | pxcloud--session-userGroups         |
| com.cisco.ise.config.anc         | statusTopic           | pxcloud--anc-operationStatus        |
| com.cisco.ise.mdm                | endpointTopic         | pxcloud--mdm-endpoints              |
| com.cisco.ise.config.profiler    | topic                 | pxcloud--profiler-profiles          |
| com.cisco.ise.radius             | failureTopic          | pxcloud--radius-failures            |
| com.cisco.ise.trustsec           | policyDownloadTopic   | pxcloud--trustsec-policyDownloads   |
| com.cisco.ise.config.trustsec    | securityGroupTopic    | pxcloud--trustsec-securityGroups    |
| com.cisco.ise.config.trustsec    | securityGroupAclTopic | pxcloud--trustsec-securityGroupAcls |
| com.cisco.ise.sxp                | bindingTopic          | pxcloud--trustsec-bindings          |
| com.cisco.ise.echo               | echoTopic             | pxcloud--echo-echo                  |


## Terminology

- `App`: An app represents an application that's available on the Cisco DNA - Cloud App Store.
- `Tenant`: A tenant represents a Cisco customer that subscribes to and intends to use services
Cisco DNA - Cloud services.
- `Device`: A device represents an on-premise entity (an appliance, a VM, a deployment, a cluster,
  etc.) that is registered with the Cisco DNA - Cloud. Cisco ISE is such an example.
