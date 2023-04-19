package main

import (
	"io"
	"k8s.io/klog/v2"
	"mime/multipart"
	"net/http"
	"os"
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
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer func(file multipart.File) {
			err := file.Close()
			if err != nil {

			}
		}(file)

		bytes, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		err = os.WriteFile("/mnt/data/file.txt", bytes, 0644)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		klog.Infof("File saved successfully.")
		return
	}

	http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
}
