# echo-query

A pxGrid Cloud app to query Cisco ISE

## Build

```bash
go build
```

This will generate a binary named `echo-query` in ths current directory.

## Run

### Show command options

```bash
./echo-query --help
Usage of ./echo-query:
  -config string
    	Configuration yaml file to use (required)
  -debug
    	Enable debug output
  -in string
    	File for input for echo (optional). stdin if not specified
  -insecure
    	Insecure TLS
  -out string
    	File for output for echo (optional). stdout if not specified

```

A configuration file is required. Refer to [`config_sample.yaml`](./config_sample.yaml) for the details.

### Run with configuration

```bash
./echo-query -config config.yaml
```

