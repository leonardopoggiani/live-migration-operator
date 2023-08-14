package main

import (
	"io"
	"mime/multipart"
	"net/http"
	"os"

	"k8s.io/klog/v2"
)

func main() {
	klog.Infof("Starting file handler...")
	http.HandleFunc("/", handleFile)
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		klog.ErrorS(err, "Failed to start file handler.")
	}
}

func handleFile(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		} else {
			klog.Infof("File received", "file", header.Filename)
		}
		defer func(file multipart.File) {
			err := file.Close()
			if err != nil {
				klog.ErrorS(err, "Failed to close file.")
			}
		}(file)

		klog.Infof("Saving file to disk...", "file", header.Filename)

		bytes, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		err = os.WriteFile("/mnt/data/"+header.Filename, bytes, 0644)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		klog.Infof("File saved successfully.")
		return
	} else if r.Method == "GET" {
		klog.Infof("Debug GET request received.")
		http.Error(w, "Debug GET request received.", http.StatusOK)
		return
	}

	http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
}
