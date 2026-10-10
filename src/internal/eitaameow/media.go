package eitaameow

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

type mediaReference struct {
	Account   string `json:"account"`
	Kind      string `json:"kind"`
	ID        int64  `json:"id"`
	Hash      int64  `json:"hash"`
	Reference []byte `json:"reference"`
	Thumb     string `json:"thumb"`
}

func (c *Client) sendMedia(ctx context.Context, r domains.SendRequest, peer object, rid int64) (result domains.SendResult, err error) {
	media, err := c.uploadMedia(ctx, r, rid)
	if err != nil {
		return result, err
	}
	defer func() {
		if err != nil {
			err = compoundMediaError(err)
		}
	}()
	params := object{"peer": peer, "media": media, "message": r.Text, "random_id": rid}
	if r.ReplyMessageID != "" {
		params["reply_to_msg_id"], _ = messageNumber(r.ReplyMessageID)
	}
	response, err := c.mediaStage(ctx, 2, "send", rid, func() (object, error) {
		response, err := c.invokeUpload(ctx, "messages.sendMedia", params)
		if err != nil {
			return nil, err
		}
		if _, err = sendResult(response, rid); err != nil {
			return nil, err
		}
		return response, nil
	})
	if err != nil {
		return result, err
	}
	return sendResult(response, rid)
}

// uploadMedia inspects metadata before the first write and returns an uploaded
// input media object. Any failure after an accepted part leaves compound work
// unknown; no transport retry or endpoint failover occurs here.
func (c *Client) uploadMedia(ctx context.Context, r domains.SendRequest, rid int64) (object, error) {
	return c.uploadMediaAt(ctx, r, rid, 1)
}
func (c *Client) uploadMediaAt(ctx context.Context, r domains.SendRequest, rid int64, stage int) (object, error) {
	var data json.RawMessage
	if r.Kind == "video" {
		data, _ = json.Marshal(object{"thumbnail_request_id": strconv.FormatInt(mediaPreviewRID(rid), 10)})
	}
	return c.mediaStageData(ctx, stage, "upload", rid, data, func() (object, error) { return c.uploadMediaBytes(ctx, r, rid) })
}
func (c *Client) uploadMediaBytes(ctx context.Context, r domains.SendRequest, rid int64) (_ object, err error) {
	if c.cfg.MediaSource == nil {
		return nil, domains.Unsupported("media source")
	}
	source, info, err := c.cfg.MediaSource(ctx, r.MediaID)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, domains.E("MEDIA_NOT_FOUND", "media is unavailable", 404)
	}
	defer source.Close()
	if info.Size < 1 || info.Size > c.cfg.MaxMediaBytes {
		return nil, domains.E("MEDIA_TOO_LARGE", "media exceeds the configured size limit", 413)
	}
	name := filepath.Base(info.Name)
	if name == "." || name == "/" || len(name) > 255 || strings.ContainsAny(name, "\x00\r\n") {
		return nil, invalidMedia()
	}
	var inspected io.Reader
	var attrs []object
	var mime string
	cleanup := func() {}
	if r.Kind == "profile_photo" {
		var photo []byte
		photo, err = prepareProfilePhoto(ctx, source, info.Size, c.cfg.MaxMediaBytes)
		inspected, info.Size, name, mime = bytes.NewReader(photo), int64(len(photo)), "profile.jpg", "image/jpeg"
	} else {
		inspected, attrs, mime, cleanup, err = prepareMedia(ctx, source, info.Size, r.Kind)
	}
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if mime == "" {
		mime = info.ContentType
	}
	if mime == "" {
		mime = "application/octet-stream"
	}

	var preview []byte
	if r.Kind == "video" {
		preview, err = videoPreview(ctx, inspected, info.Size)
		if err != nil {
			return nil, err
		}
	}
	reader := bufio.NewReaderSize(inspected, 64<<10)
	const partSize = 64 << 10
	parts := (info.Size + partSize - 1) / partSize
	big := info.Size >= 10<<20
	if parts > 16384 {
		return nil, domains.E("MEDIA_TOO_LARGE", "too many Eitaa upload parts", 413)
	}
	uploaded := false
	defer func() {
		if uploaded && err != nil {
			err = compoundMediaError(err)
		}
	}()
	remaining := info.Size
	for i := int64(0); i < parts; i++ {
		size := min(int64(partSize), remaining)
		chunk := make([]byte, int(size))
		if _, err = io.ReadFull(reader, chunk); err != nil {
			return nil, invalidMedia()
		}
		params := object{"file_id": rid, "file_part": i, "bytes": chunk}
		if i == 0 {
			params["totalFileSize"] = info.Size
			params["peer"] = c.uploadPeer(r.Peer)
		}
		method := "upload.saveFilePart"
		if big {
			method = "upload.saveBigFilePart"
			params["file_total_parts"] = parts
		}
		result, e := c.invokeUpload(ctx, method, params)
		if e != nil {
			return nil, e
		}
		if result["acknowledged"] != true {
			return nil, protocolError()
		}
		uploaded = true
		remaining -= size
	}
	if _, e := reader.ReadByte(); e != io.EOF {
		return nil, invalidMedia()
	}
	file := object{"_": "inputFile", "id": rid, "parts": parts, "name": name, "md5_checksum": ""}
	if big {
		file = object{"_": "inputFileBig", "id": rid, "parts": parts, "name": name}
	}
	if r.Kind == "image" || r.Kind == "profile_photo" {
		return object{"_": "inputMediaUploadedPhoto", "file": file}, nil
	}
	attrs = append(attrs, object{"_": "documentAttributeFilename", "file_name": name})
	media := object{"_": "inputMediaUploadedDocument", "file": file, "mime_type": mime, "attributes": attrs}
	if len(preview) > 0 {
		// The official web uploader associates thumbnail parts with the original
		// video's totalFileSize, rather than the thumbnail's own byte length.
		previewID := mediaPreviewRID(rid)
		ack, e := c.invokeUpload(ctx, "upload.saveFilePart", object{"file_id": previewID, "file_part": 0, "bytes": preview, "totalFileSize": info.Size, "peer": c.uploadPeer(r.Peer)})
		if e != nil {
			return nil, e
		}
		if ack["acknowledged"] != true {
			return nil, protocolError()
		}
		media["thumb"] = object{"_": "inputFile", "id": previewID, "parts": 1, "name": "preview.jpg", "md5_checksum": ""}
	}
	if r.Kind == "file" {
		media["force_file"] = true
	}
	return media, nil
}

func (c *Client) uploadPeer(p domains.Peer) object { return plainPeer(p) }

func plainPeer(p domains.Peer) object {
	kind, key := "peerUser", "user_id"
	if p.Type == "group" {
		kind, key = "peerChat", "chat_id"
	}
	if p.Type == "channel" || isSupergroup(p) {
		kind, key = "peerChannel", "channel_id"
	}
	n, _ := strconv.ParseInt(peerWireID(p), 10, 64)
	return object{"_": kind, key: n}
}

func (c *Client) projectMedia(m object) (*domains.ProviderMedia, *domains.MessageMedia) {
	var file object
	var kind, mime, name, thumb string
	var size int64
	switch m.str("_") {
	case "messageMediaDocument":
		file = asObject(m["document"])
		kind = "file"
		size = file.num("size")
		mime = file.str("mime_type")
		name = "document-" + strconv.FormatInt(file.num("id"), 10)
		for _, a := range asObjects(file["attributes"]) {
			switch a.str("_") {
			case "documentAttributeFilename":
				name = filepath.Base(a.str("file_name"))
			case "documentAttributeAudio":
				kind = "audio"
				if a["voice"] == true {
					kind = "voice"
				}
			case "documentAttributeVideo":
				kind = "video"
			case "documentAttributeSticker":
				kind = "sticker"
			}
		}
	case "messageMediaPhoto":
		file = asObject(m["photo"])
		kind = "image"
		mime = "image/jpeg"
		name = "photo-" + strconv.FormatInt(file.num("id"), 10) + ".jpg"
		for _, s := range asObjects(file["sizes"]) {
			if s.num("size") > size {
				size = s.num("size")
				thumb = s.str("type")
			}
		}
	default:
		return nil, nil
	}
	if file == nil || file.num("id") == 0 || size < 1 || size > c.cfg.MaxMediaBytes || len(name) > 255 || strings.ContainsAny(name, "\x00\r\n") {
		return nil, nil
	}
	reference, ok := file["file_reference"].([]byte)
	if !ok || len(reference) > 64<<10 {
		return nil, nil
	}
	if _, ok := file["access_hash"]; !ok {
		return nil, nil
	}
	c.mu.RLock()
	account := c.session.UserID
	c.mu.RUnlock()
	ref := mediaReference{Account: account, Kind: "document", ID: file.num("id"), Hash: file.num("access_hash"), Reference: reference, Thumb: thumb}
	if kind == "image" {
		ref.Kind = "photo"
	}
	data, _ := json.Marshal(ref)
	id := strconv.FormatInt(ref.ID, 10)
	return &domains.ProviderMedia{Provider: domains.ProviderEitaa, Version: 1, Data: data, FileID: id, Size: size, Name: name, ContentType: mime}, &domains.MessageMedia{Type: kind, FileID: id, Name: name, MIMEType: mime, Size: size}
}

type cancelReader struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (r *cancelReader) Close() error { r.cancel(); return r.PipeReader.Close() }
func (c *Client) Download(ctx context.Context, m domains.ProviderMedia) (io.ReadCloser, error) {
	var ref mediaReference
	if m.Provider != domains.ProviderEitaa || m.Version != 1 || len(m.Data) > 128<<10 || json.Unmarshal(m.Data, &ref) != nil || m.Size < 1 || m.Size > c.cfg.MaxMediaBytes || ref.ID == 0 || strconv.FormatInt(ref.ID, 10) != m.FileID || (ref.Kind != "document" && ref.Kind != "photo") || len(ref.Reference) > 64<<10 {
		return nil, domains.E("INVALID_MEDIA_REFERENCE", "invalid Eitaa media reference", 400)
	}
	c.mu.RLock()
	account := c.session.UserID
	c.mu.RUnlock()
	if ref.Account != account {
		return nil, domains.E("MEDIA_SCOPE_MISMATCH", "media belongs to another Eitaa account", 409)
	}
	location := object{"_": "inputDocumentFileLocation", "id": ref.ID, "access_hash": ref.Hash, "file_reference": ref.Reference, "thumb_size": ref.Thumb}
	if ref.Kind == "photo" {
		location["_"] = "inputPhotoFileLocation"
	}
	streamCtx, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	go func() {
		defer cancel()
		var result error
		defer func() { writer.CloseWithError(result) }()
		for offset := int64(0); offset < m.Size; {
			limit := min(int64(512<<10), m.Size-offset)
			o, e := c.invoke(streamCtx, "upload.getFile", object{"location": location, "offset": offset, "limit": limit}, false, false)
			if e != nil {
				result = e
				return
			}
			data, ok := o["bytes"].([]byte)
			if o.str("_") != "upload.file" || !ok || len(data) == 0 || int64(len(data)) > limit {
				result = protocolError()
				return
			}
			if _, e = writer.Write(data); e != nil {
				result = e
				return
			}
			offset += int64(len(data))
		}
	}()
	return &cancelReader{PipeReader: reader, cancel: cancel}, nil
}
