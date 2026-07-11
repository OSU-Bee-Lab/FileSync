package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// remoteFieldsForm is the scaffold shared by the "Add Location" wizard and
// the "Edit Location" screen for collecting a remote's backend-specific
// settings: a "Path within remote" entry + Browse button, and the
// per-backend FieldSpec form (populateRemoteFields) folded into a collapsed
// "Advanced options" accordion whenever any field is Advanced.
//
// pathEntry and browseBtn are exposed separately from container (rather
// than baked into it) because not every caller shows the path row in the
// same place - the wizard's OneDrive/SharePoint branch omits it entirely,
// since that path comes from browsing a chosen document library rather
// than being typed. browseBtn's OnTapped is left unset by
// newRemoteFieldsForm: what "Browse" needs to do first differs between the
// two screens (the wizard may need to create and authorize the remote
// before it can list folders; the edit screen's remote already exists).
type remoteFieldsForm struct {
	container    *fyne.Container
	pathEntry    *widget.Entry
	browseBtn    *widget.Button
	fieldWidgets map[string]fyne.CanvasObject

	remoteFieldsBox   *fyne.Container
	advancedFieldsBox *fyne.Container
	advancedAccordion *widget.Accordion
	// prefill is nil for a brand-new remote (the wizard) or the remote's
	// current non-secret values (the edit screen); see readFields.
	prefill map[string]string
}

// newRemoteFieldsForm builds the scaffold and renders bt's fields into it.
// prefill overrides a field's rclone-reported default when present - the
// edit screen passes the remote's current values so the form starts
// showing them, the wizard passes nil so every field starts at its backend
// default.
func newRemoteFieldsForm(s *state, bt syncengine.BackendType, prefill map[string]string) *remoteFieldsForm {
	f := &remoteFieldsForm{
		fieldWidgets:      map[string]fyne.CanvasObject{},
		remoteFieldsBox:   container.NewVBox(),
		advancedFieldsBox: container.NewVBox(),
		prefill:           prefill,
	}
	f.advancedAccordion = widget.NewAccordion(widget.NewAccordionItem("Advanced options", f.advancedFieldsBox))
	f.pathEntry = widget.NewEntry()
	f.browseBtn = widget.NewButton("Browse...", nil)
	f.container = container.NewVBox(f.remoteFieldsBox)
	f.setBackend(s, bt)
	return f
}

// setBackend (re-)renders the FieldSpecs for bt, clearing out any previous
// backend's fields first. It leaves pathEntry and browseBtn untouched, so
// the wizard can call this each time the user changes the "Type" kind
// picker without losing a path they already typed.
func (f *remoteFieldsForm) setBackend(s *state, bt syncengine.BackendType) {
	populateRemoteFields(s, bt, f.prefill, f.remoteFieldsBox, f.advancedFieldsBox, f.fieldWidgets)
	f.container.Objects = []fyne.CanvasObject{f.remoteFieldsBox}
	if len(f.advancedFieldsBox.Objects) > 0 {
		f.container.Objects = append(f.container.Objects, f.advancedAccordion)
	}
	f.container.Refresh()
}

// pathRow returns the "Path within remote" form row (entry + Browse
// button). Every backend except OneDrive/SharePoint shows this.
func (f *remoteFieldsForm) pathRow() fyne.CanvasObject {
	return widget.NewForm(&widget.FormItem{Text: "Path within remote", Widget: container.NewBorder(nil, nil, nil, f.browseBtn, f.pathEntry)})
}

// fieldText reads back whichever widget type populateRemoteFields chose to
// render for a given FieldSpec.
func fieldText(w fyne.CanvasObject) string {
	switch e := w.(type) {
	case *widget.Entry:
		return e.Text
	case *widget.Select:
		return e.Selected
	}
	return ""
}

// readFields reads back the current value of every spec's widget. The
// semantics depend on prefill (as passed to newRemoteFieldsForm):
//
//   - nil prefill (the wizard, populating a brand-new remote): every
//     field's value is included as typed, secrets included too, and
//     changed is unconditionally true.
//   - non-nil prefill (the edit screen): a secret left blank means "leave
//     the existing credential alone" and is omitted from values entirely
//     (UpdateRemote only touches keys present in the map); changed reports
//     whether anything actually differs from prefill (a typed secret
//     always counts, since prefill never carries secrets to compare
//     against).
func (f *remoteFieldsForm) readFields(specs []syncengine.FieldSpec) (values map[string]string, changed bool) {
	values = map[string]string{}
	for _, spec := range specs {
		w, ok := f.fieldWidgets[spec.Key]
		if !ok {
			continue
		}
		v := fieldText(w)
		if f.prefill == nil {
			values[spec.Key] = v
			changed = true
			continue
		}
		if spec.IsSecret && v == "" {
			continue
		}
		values[spec.Key] = v
		if spec.IsSecret || f.prefill[spec.Key] != v {
			changed = true
		}
	}
	return values, changed
}
