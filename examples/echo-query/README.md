# basic-consumer

A pxGrid Cloud app to query Cisco ISE

## Build

```bash
go build
```

This will generate a binary named `echo-query` in ths current directory.

## Run

### Show command options

```bash
./echo-query -help

Usage of ./echo-query:
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
./echo-query -config config.yaml
```

