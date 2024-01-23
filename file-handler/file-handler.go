package main

import (
	"io"
	"os"
	"strings"

	"github.com/valyala/fasthttp"
	"k8s.io/klog/v2"
)

func main() {
	klog.Infof("Starting file handler...")

	m := func(ctx *fasthttp.RequestCtx) {
		switch string(ctx.Path()) {
		case "/upload":
			handleFile(ctx)
		default:
			ctx.Error("not found", fasthttp.StatusNotFound)
		}
	}

	s := &fasthttp.Server{
		Handler:            m,
		Name:               "File handler",
		MaxRequestBodySize: 4 * 1024 * 1024 * 1024,
	}

	err := s.ListenAndServe(":8080")
	if err != nil {
		klog.ErrorS(err, "Failed to start file handler.")
	}
}

func handleFile(ctx *fasthttp.RequestCtx) {
	if strings.EqualFold(string(ctx.Method()), "POST") {
		file, err := ctx.FormFile("file")
		if err != nil {
			ctx.Error("Unsupported path", fasthttp.StatusBadRequest)
			return
		} else {
			klog.Infof("File received %s", file.Filename)
		}

		klog.Info("Saving file to disk...", "file", file.Filename)
		opened, err := file.Open()
		if err != nil {
			ctx.Error("Failed to open file", fasthttp.StatusInternalServerError)
			return
		}
		defer opened.Close()

		destinationPath := "/mnt/data/" + file.Filename
		dstFile, err := os.Create(destinationPath)
		if err != nil {
			ctx.Error("Failed to create destination file", fasthttp.StatusInternalServerError)
			return
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, opened)
		if err != nil {
			ctx.Error("Failed to copy file contents", fasthttp.StatusInternalServerError)
			return
		}

		klog.Infof("File saved successfully.")
		return
	} else if strings.EqualFold(string(ctx.Method()), "GET") {
		klog.Infof("Debug GET request received.")
		ctx.SetStatusCode(fasthttp.StatusOK)
		return
	}

	ctx.Error("Method not allowed.", fasthttp.StatusMethodNotAllowed)
}
