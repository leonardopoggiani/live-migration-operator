# Immagine di partenza
FROM ubuntu:latest

# Installare VLC
RUN apt-get update && \
    apt-get install -y --no-install-recommends vlc vlc-plugins-*

# Copiare il file video.mp4 nella directory di lavoro
COPY video.mp4 /app/video.mp4

# Impostare la directory di lavoro
WORKDIR /app

# Comando per eseguire il video con VLC
ENTRYPOINT ["vlc" , "video.mp4"]