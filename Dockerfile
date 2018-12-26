FROM golang:alpine as builder

ENV PATH /go/bin:/usr/local/go/bin:$PATH
ENV GOPATH /go

COPY . /go/src/github.com/virtual-kubelet-webhook
WORKDIR /go/src/github.com/virtual-kubelet-webhook

RUN go build ./

RUN cp ./virtual-kubelet-webhook /usr/bin/virtual-kubelet-webhook

ENTRYPOINT [ "/usr/bin/virtual-kubelet-webhook" ]
