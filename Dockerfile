FROM golang:1.20 as builder
LABEL authors="darkknight"

WORKDIR /app

COPY . .

RUN go env -w GOPROXY=https://goproxy.cn,direct

RUN CGO_ENABLED=0 go build -o ingress-manager main.go

FROM alpine:3.18.4

WORKDIR /app

COPY --from=builder /app/ingress-manager .

CMD ["./ingress-manager"]


