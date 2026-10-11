package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/labstack/echo/v4"
	"github.com/mudler/localrecall/rag/raw"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func rawFailure(c echo.Context, err error) error {
	e := raw.NewError(raw.CodeInternal)
	var re *raw.Error
	if errors.As(err, &re) {
		e = raw.NewError(re.Code)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		e = raw.NewError(raw.CodeTimeout)
		// Do not drain an incomplete timed-out body before sending the error.
		c.Response().Header().Set("Connection", "close")
	}
	return c.JSON(e.Status, struct {
		Success bool       `json:"success"`
		Error   *raw.Error `json:"error"`
	}{false, e})
}
func rawBody(c echo.Context, dst any) error {
	b, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, raw.MaxBodyBytes))
	if c.Request().Context().Err() != nil {
		return c.Request().Context().Err()
	}
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			return raw.NewError(raw.CodeBodyLimit)
		}
		return raw.NewError(raw.CodeValidation)
	}
	if err := raw.DecodeRequest(b, dst); err != nil {
		return err
	}
	// NUL is invalid in every PostgreSQL-bound string, including map keys.
	decoder := json.NewDecoder(bytes.NewReader(b))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return raw.NewError(raw.CodeValidation)
		}
		if text, ok := token.(string); ok && strings.ContainsRune(text, 0) {
			return raw.NewError(raw.CodeValidation)
		}
	}
	return nil
}

// registerRawAPIRoutes inherits the instance middleware installed by registerAPIRoutes.
// A nil store denotes a failed bootstrap, never a fallback to legacy persistence.
func registerRawAPIRoutes(e *echo.Echo, store raw.Store) {
	g := e.Group("/api/raw/v1")
	g.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx, cancel := context.WithTimeout(c.Request().Context(), 30*time.Second)
			defer cancel()
			c.SetRequest(c.Request().WithContext(ctx))
			// Incoming Body.Read does not observe context cancellation. Interrupt
			// the transport only for raw requests, including early cancellation.
			controller := http.NewResponseController(c.Response().Writer)
			interrupted := make(chan struct{})
			stop := context.AfterFunc(ctx, func() {
				_ = controller.SetReadDeadline(time.Now())
				close(interrupted)
			})
			defer func() {
				if !stop() {
					<-interrupted
				}
				// Do not leak a raw deadline into a subsequent keep-alive request.
				_ = controller.SetReadDeadline(time.Time{})
			}()

			if store == nil && c.Path() != "/api/raw/v1/capabilities" {
				if vectorEngine != "postgres" {
					return rawFailure(c, raw.NewError(raw.CodeUnsupported))
				}
				return rawFailure(c, raw.NewError(raw.CodeUnavailable))
			}
			return next(c)
		}
	})
	g.GET("/capabilities", func(c echo.Context) error {
		if store == nil {
			caps := raw.Capabilities{Backend: vectorEngine, RawCollections: vectorEngine == "postgres", Readiness: raw.Readiness{Reason: "Required dependency unavailable"}, Limits: raw.ContractLimits()}
			if caps.RawCollections {
				caps.Modes = []string{"vector", "lexical", "hybrid"}
				caps.AccuracyOptions = []string{"approximate", "exact"}
				caps.DefaultAccuracy = "approximate"
				caps.Metrics = []string{"cosine"}
				caps.AtomicBatch = true
				caps.Persistent = true
			}
			return c.JSON(200, successResponse("", caps))
		}
		data, err := store.Capabilities(c.Request().Context())
		if err != nil {
			return rawFailure(c, err)
		}
		return c.JSON(200, successResponse("", data))
	})
	g.POST("/collections", func(c echo.Context) error {
		var req raw.CreateCollectionRequest
		if err := rawBody(c, &req); err != nil {
			return rawFailure(c, err)
		}
		data, created, err := store.CreateCollection(c.Request().Context(), req)
		if err != nil {
			return rawFailure(c, err)
		}
		status := 200
		if created {
			status = 201
		}
		return c.JSON(status, successResponse("", data))
	})
	g.GET("/collections", func(c echo.Context) error {
		var req raw.ListCollectionsRequest
		for k, v := range c.QueryParams() {
			if len(v) != 1 {
				return rawFailure(c, raw.NewError(raw.CodeValidation))
			}
			switch k {
			case "limit":
				n, err := strconv.Atoi(v[0])
				if err != nil {
					return rawFailure(c, raw.NewError(raw.CodeValidation))
				}
				req.Limit = &n
			case "cursor":
				req.Cursor = &v[0]
			default:
				return rawFailure(c, raw.NewError(raw.CodeValidation))
			}
		}
		data, err := store.ListCollections(c.Request().Context(), req)
		if err != nil {
			return rawFailure(c, err)
		}
		return c.JSON(200, successResponse("", data))
	})
	g.GET("/collections/:cid", func(c echo.Context) error {
		data, err := store.GetCollection(c.Request().Context(), c.Param("cid"))
		if err != nil {
			return rawFailure(c, err)
		}
		return c.JSON(200, successResponse("", data))
	})
	g.POST("/collections/:cid/records/upsert", func(c echo.Context) error {
		var req raw.UpsertRequest
		if err := rawBody(c, &req); err != nil {
			return rawFailure(c, err)
		}
		data, err := store.Upsert(c.Request().Context(), c.Param("cid"), req)
		if err != nil {
			return rawFailure(c, err)
		}
		return c.JSON(200, successResponse("", data))
	})
	g.POST("/collections/:cid/records/get", func(c echo.Context) error {
		var req raw.GetRecordsRequest
		if err := rawBody(c, &req); err != nil {
			return rawFailure(c, err)
		}
		data, err := store.GetRecords(c.Request().Context(), c.Param("cid"), req)
		if err != nil {
			return rawFailure(c, err)
		}
		return c.JSON(200, successResponse("", data))
	})
	g.POST("/collections/:cid/records/list", func(c echo.Context) error {
		var req raw.ListRecordsRequest
		if err := rawBody(c, &req); err != nil {
			return rawFailure(c, err)
		}
		data, err := store.ListRecords(c.Request().Context(), c.Param("cid"), req)
		if err != nil {
			return rawFailure(c, err)
		}
		return c.JSON(200, successResponse("", data))
	})
	g.POST("/collections/:cid/records/delete", func(c echo.Context) error {
		var req raw.DeleteRecordsRequest
		if err := rawBody(c, &req); err != nil {
			return rawFailure(c, err)
		}
		data, err := store.DeleteRecords(c.Request().Context(), c.Param("cid"), req)
		if err != nil {
			return rawFailure(c, err)
		}
		return c.JSON(200, successResponse("", data))
	})
	g.POST("/collections/:cid/search", func(c echo.Context) error {
		var req raw.SearchRequest
		if err := rawBody(c, &req); err != nil {
			return rawFailure(c, err)
		}
		data, err := store.Search(c.Request().Context(), c.Param("cid"), req)
		if err != nil {
			return rawFailure(c, err)
		}
		return c.JSON(200, successResponse("", data))
	})
	g.POST("/collections/:cid/reset", func(c echo.Context) error {
		var req raw.ResetRequest
		if err := rawBody(c, &req); err != nil {
			return rawFailure(c, err)
		}
		if err := store.Reset(c.Request().Context(), c.Param("cid")); err != nil {
			return rawFailure(c, err)
		}
		return c.JSON(200, successResponse("", raw.ResetResponse{Reset: true}))
	})
	g.DELETE("/collections/:cid", func(c echo.Context) error {
		b, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, raw.MaxBodyBytes))
		if c.Request().Context().Err() != nil {
			return rawFailure(c, c.Request().Context().Err())
		}
		if err != nil {
			var limit *http.MaxBytesError
			if errors.As(err, &limit) {
				return rawFailure(c, raw.NewError(raw.CodeBodyLimit))
			}
			return rawFailure(c, raw.NewError(raw.CodeValidation))
		}
		if len(b) != 0 {
			return rawFailure(c, raw.NewError(raw.CodeValidation))
		}
		if err := store.DeleteCollection(c.Request().Context(), c.Param("cid")); err != nil {
			return rawFailure(c, err)
		}
		return c.JSON(200, successResponse("", raw.DeleteCollectionResponse{Deleted: true}))
	})
}
