# Basic

A basic pxGrid Cloud app to receive messages and make queries to Cisco ISE

## Build

```bash
go build
```

This will generate a binary named `basic` in ths current directory.

## Run

### Show command options

```bash
./basic -help

Usage of ./basic:
  -config string
    	Configuration yaml file to use (required)
  -debug
    	Enable debug output
  -insecure
    	Insecure TLS
```

A configuration file is required. Refer to [`config_sample.yaml`](./config_sample.yaml) for the details.

### Run with configuration

```bash
./basic -config config.yaml
```

