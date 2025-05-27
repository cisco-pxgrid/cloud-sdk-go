package cloud

import (
	"errors"
	"fmt"

	"github.com/rs/xid"
)

type appInstanceRequest struct {
	OTP          string `json:"otp"`
	ParentAppID  string `json:"parent_application_id"`
	InstanceID   string `json:"instance_identifier"`
	InstanceName string `json:"instance_name"`
}

type appInstanceResponse struct {
	AppID       string `json:"app_id"`
	AppApiKey   string `json:"app_api_key"`
	TenantToken string `json:"api_token"`
	TenantID    string `json:"tenant_id"`
	TenantName  string `json:"tenant_name"`
}

type createAppInstanceRequest struct {
	InstanceID   string `json:"instance_identifier"`
	InstanceName string `json:"instance_name"`
}

type createAppInstanceResponse struct {
	ID     string `json:"application_id"`
	ApiKey string `json:"api_key"`
}

// LinkTenantWithNewAppInstance redeems the OTP and links a tenant to a new application instance.
// InstanceName will be shown in UI to signify the new application instance.
// The returned	App.ID(), App.ApiKey(), Tenant.ID(), Tenant.Name() and Tenant.ApiToken() must be stored securely.
//
// Deprecated: Use CreateAppInstance and LinkTenant instead.
func (app *App) LinkTenantWithNewAppInstance(otp, instanceName string) (*App, *Tenant, error) {
	appReq := appInstanceRequest{
		OTP:          otp,
		ParentAppID:  app.config.ID,
		InstanceID:   xid.New().String(),
		InstanceName: instanceName,
	}

	var appResp appInstanceResponse
	var errorResp errorResponse

	r, err := app.httpClient.R().
		SetBody(appReq).
		SetResult(&appResp).
		SetError(&errorResp).
		Post(newAppInstancePath)
	if err != nil {
		return nil, nil, err
	}
	if r.IsError() {
		return nil, nil, errors.New(errorResp.GetError())
	}

	appConfig := app.newAppConfig(appResp.AppID, appResp.AppApiKey)

	childApp, err := New(appConfig)
	if err != nil {
		return nil, nil, err
	}
	tenant, err := childApp.SetTenant(appResp.TenantID, appResp.TenantName, appResp.TenantToken)
	if err != nil {
		return nil, nil, err
	}
	return childApp, tenant, nil
}

// CreateAppInstance creates an app instance. The instance can then be used to LinkTenant.
// This is called with the parent app, using the parent app_key to authenticate
// InstanceName will be shown in UI to signify the new application instance.
// The returned	App.ID() and App.ApiKey() must be stored securely.
// This can only be used with application that is registered as multi-instance.
func (app *App) CreateAppInstance(instanceName string) (*App, error) {
	req := createAppInstanceRequest{
		InstanceID:   xid.New().String(),
		InstanceName: instanceName,
	}

	var resp createAppInstanceResponse
	var errorResp errorResponse

	path := fmt.Sprintf(createAppInstancePath, app.config.ID)

	r, err := app.httpClient.R().
		SetBody(req).
		SetResult(&resp).
		SetError(&errorResp).
		Post(path)
	if err != nil {
		return nil, err
	}
	if r.IsError() {
		return nil, errors.New(errorResp.GetError())
	}

	appConfig := app.newAppConfig(resp.ID, resp.ApiKey)
	return New(appConfig)
}

// DeleteAppInstance deletes an app instance using the AppId of the instance
// This is called with the parent app, using the parent app_key to authenticate.
func (app *App) DeleteAppInstance(instanceAppId string) error {
	var errorResp errorResponse
	path := fmt.Sprintf(deleteAppInstancePath, instanceAppId)
	r, err := app.httpClient.R().
		SetError(&errorResp).
		Delete(path)
	if err != nil {
		return err
	}
	if r.IsError() {
		return errors.New(errorResp.GetError())
	}
	return nil
}

// SetAppInstance adds an application instance.
// The appID and appApiKey are obtained from calling LinkTenantWithNewAppInstance.
// This should be used by application after restart to reload application instances
func (app *App) SetAppInstance(appID, appApiKey string) (*App, error) {
	config := app.newAppConfig(appID, appApiKey)
	return New(config)
}

func (app *App) newAppConfig(appID, appApiKey string) Config {
	return Config{
		ID:                        appID,
		GlobalFQDN:                app.config.GlobalFQDN,
		RegionalFQDN:              app.config.RegionalFQDN,
		RegionalFQDNs:             app.config.RegionalFQDNs,
		ReadStreamID:              "app--" + appID + "-R",
		WriteStreamID:             "app--" + appID + "-W",
		GroupID:                   app.config.GroupID,
		Transport:                 app.config.Transport,
		ApiKey:                    appApiKey,
		DeviceActivationHandler:   app.config.DeviceActivationHandler,
		DeviceDeactivationHandler: app.config.DeviceDeactivationHandler,
		TenantUnlinkedHandler:     app.config.TenantUnlinkedHandler,
		DeviceMessageHandler:      app.config.DeviceMessageHandler,
	}
}

func (app *App) ID() string {
	return app.config.ID
}

func (app *App) ApiKey() string {
	return app.config.ApiKey
}
