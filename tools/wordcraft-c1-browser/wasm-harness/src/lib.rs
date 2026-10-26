//! Test/developer harness over the ACTUAL pinned native UI registry and C1 seam.
//! No transport, permissions, SQL, host Saved or real human qualification.
use std::{
    cell::{Cell, RefCell},
    rc::Rc,
};
use wasm_bindgen::prelude::*;
use wordcraft_doc::{Path, Pos, StoryRef};
use wordcraft_engine::{Selection, Session, io as native_io};
use wordcraft_ui_egui::{
    Services, WordApp,
    c1_commands::{CommandBridge, native_document},
};
#[wasm_bindgen]
pub struct C1Harness {
    app: WordApp,
    bridge: Rc<CommandBridge>,
    captured: Rc<RefCell<Vec<String>>>,
    capture_ok: Rc<Cell<bool>>,
    gate: Rc<Cell<bool>>,
}
#[wasm_bindgen]
impl C1Harness {
    #[wasm_bindgen(constructor)]
    pub fn new(native_json: &str, ids_json: &str) -> Result<Self, String> {
        let doc = native_document(native_json)?;
        let captured = Rc::new(RefCell::new(vec![]));
        let received = captured.clone();
        let capture_ok = Rc::new(Cell::new(true));
        let accept = capture_ok.clone();
        let gate = Rc::new(Cell::new(true));
        let allowed = gate.clone();
        let bridge = CommandBridge::new(
            &doc,
            ids_json,
            Box::new(move || allowed.get()),
            Box::new(move |raw| {
                received.borrow_mut().push(raw.into());
                if accept.get() {
                    Ok(())
                } else {
                    Err("fixture capture failure".into())
                }
            }),
        )?;
        let mut app = WordApp::new(Session::new(doc), Services::default());
        app.attach_c1_commands(bridge.clone())?;
        Ok(Self {
            app,
            bridge,
            captured,
            capture_ok,
            gate,
        })
    }
    pub fn run_registry(&mut self, id: &str, params_json: &str) -> Result<String, String> {
        let params = serde_json::from_str(params_json).map_err(|_| "invalid harness params")?;
        serde_json::to_string(&self.app.run(id, params)?).map_err(|_| "harness encode".into())
    }
    pub fn command_enabled(&self, id: &str) -> bool {
        wordcraft_ui_egui::widgets::enabled(&self.app, id)
    }
    pub fn set_selection(&mut self, block: u32, start: usize, end: usize) {
        self.app.session.sel = Selection {
            anchor: Pos {
                story: StoryRef::Body,
                path: Path(vec![block]),
                off: start,
            },
            focus: Pos {
                story: StoryRef::Body,
                path: Path(vec![block]),
                off: end,
            },
        };
    }
    pub fn project_confirmed(
        &mut self,
        native_json: &str,
        ids_json: &str,
        acknowledges_pending: bool,
        confirmed_selection_json: &str,
    ) -> Result<(), String> {
        self.app.project_c1_confirmed(
            native_json,
            ids_json,
            acknowledges_pending,
            confirmed_selection_json,
        )
    }
    pub fn native_json(&self) -> Result<String, String> {
        String::from_utf8(native_io::save_bytes("state.json", &self.app.session.doc)?)
            .map_err(|_| "harness encode".into())
    }
    pub fn captured_json(&self) -> Result<String, String> {
        serde_json::to_string(&*self.captured.borrow()).map_err(|_| "harness encode".into())
    }
    pub fn selection_json(&self) -> Result<String, String> {
        serde_json::to_string(&self.app.session.sel).map_err(|_| "harness encode".into())
    }
    pub fn stable_ids_json(&self) -> Result<String, String> {
        self.bridge.stable_ids_json()
    }
    pub fn document_identity(&self) -> String {
        self.app.session.document_id().to_string()
    }
    pub fn pending(&self) -> bool {
        self.bridge.pending()
    }
    pub fn status(&self) -> String {
        self.app
            .status_msg
            .as_ref()
            .map(|v| v.0.clone())
            .unwrap_or_default()
    }
    pub fn set_capture_ok(&self, allowed: bool) {
        self.capture_ok.set(allowed);
    }
    pub fn set_allowed(&self, allowed: bool) {
        self.gate.set(allowed);
    }
    pub fn stop(&self) {
        self.bridge.stop();
    }
}
