package main

import "embed"

//go:embed templates/* templates/etcd-tree/* templates/etcd-tree/*/* templates/etcd-tree/*/*/*
var templateFS embed.FS
