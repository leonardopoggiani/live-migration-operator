#!/bin/bash
# Two facultative parameters : -f <nameOfTheVideoFile> and -i <URL_Output>
SCRIPTPATH=$( cd $(dirname $0) ; pwd -P );
name=$SCRIPTPATH"/video.mp4";
sdp="rtsp://:8554/flux";
while getopts "f:i:" OPTNAME
do
   case $OPTNAME in
      f) name=$OPTARG;;
      i) sdp=$OPTARG;;
   esac
done
sout="#transcode{vb=0,scale=0,acodec=mpga,ab=128,channels=2,samplerate=44100}:rtp{sdp="$sdp"}";
su vlcuser -c "vlc -vvv $name --sout '$sout'"
