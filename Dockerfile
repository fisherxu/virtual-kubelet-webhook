FROM golang:alpine as builder

ENV PATH /go/bin:/usr/local/go/bin:$PATH
ENV GOPATH /go

COPY . /go/src/github.com/virtual-kubelet-webhook
WORKDIR /go/src/github.com/virtual-kubelet-webhook

RUN go build cmd/

RUN cp cmd/cmd /usr/bin/virtual-kubelet-webhook