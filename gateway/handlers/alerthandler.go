// License: OpenFaaS Community Edition (CE) EULA
// Copyright (c) 2017,2019-2024 OpenFaaS Author(s)

// Copyright (c) Alex Ellis 2017. All rights reserved.

package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"

	"github.com/openfaas/faas/gateway/pkg/middleware"
	"github.com/openfaas/faas/gateway/requests"
	"github.com/openfaas/faas/gateway/scaling"
)

// MakeAlertHandler handles alerts from Prometheus Alertmanager
func MakeAlertHandler(service scaling.ServiceQuery, defaultNamespace string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		if r.Body == nil {
			http.Error(w, "A body is required for this endpoint", http.StatusBadRequest)
			return
		}

		defer r.Body.Close()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Unable to read alert."))

			log.Println(err)
			return
		}

		var req requests.PrometheusAlert
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Unable to parse alert, bad format."))
			log.Println(err)
			return
		}

		errors := handleAlerts(req, service, defaultNamespace)
		if len(errors) > 0 {
			log.Println(errors)
			var errorOutput string
			for d, err := range errors {
				errorOutput += fmt.Sprintf("[%d] %s\n", d, err)
			}
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(errorOutput))
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}

func handleAlerts(req requests.PrometheusAlert, service scaling.ServiceQuery, defaultNamespace string) []error {
	var errors []error
	for _, alert := range req.Alerts {
		if err := scaleService(alert, service, defaultNamespace); err != nil {
			log.Println(err)
			errors = append(errors, err)
		}
	}

	return errors
}

func scaleService(alert requests.PrometheusInnerAlert, service scaling.ServiceQuery, defaultNamespace string) error {
	var err error

	serviceName, namespace := middleware.GetNamespace(defaultNamespace, alert.Labels.FunctionName)

	if len(serviceName) > 0 {
		queryResponse, getErr := service.GetReplicas(serviceName, namespace)
		if getErr == nil {
			status := alert.Status

			newReplicas := CalculateReplicas(status, queryResponse.Replicas, uint64(queryResponse.MaxReplicas), queryResponse.MinReplicas, queryResponse.ScalingFactor)

			log.Printf("[Scale] function=%s %d => %d.\n", serviceName, queryResponse.Replicas, newReplicas)
			if newReplicas == queryResponse.Replicas {
				return nil
			}

			updateErr := service.SetReplicas(serviceName, namespace, newReplicas)
			if updateErr != nil {
				err = updateErr
			}
		}
	}
	return err
}

// CalculateReplicas decides what replica count to set depending on current/desired amount
func CalculateReplicas(status string, currentReplicas uint64, maxReplicas uint64, minReplicas uint64, scalingFactor uint64) uint64 {
	var newReplicas uint64

	maxReplicas = uint64(math.Min(float64(maxReplicas), float64(scaling.DefaultMaxReplicas)))
	step := uint64(math.Ceil(float64(maxReplicas) / 100 * float64(scalingFactor)))

	if status == "firing" && step > 0 {
		if currentReplicas+step > maxReplicas {
			newReplicas = maxReplicas
		} else {
			newReplicas = currentReplicas + step
		}
	} else { // Resolved event.
		newReplicas = minReplicas
	}

	return newReplicas
}
