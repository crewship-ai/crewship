#!/usr/bin/env python3
"""Opt-in UNO acceptance against owned synthetic S0 files, never a shared Office process."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import time
import uuid
import uno
from com.sun.star.beans import PropertyValue


def prop(name, value):
    return PropertyValue(Name=name, Value=value)


def url(path):
    return Path(path).resolve().as_uri()


def capture_shape(shape):
    result = {"name": shape.Name, "type": shape.getShapeType(), "x": shape.Position.X,
              "y": shape.Position.Y, "width": shape.Size.Width, "height": shape.Size.Height}
    try:
        result["text"] = shape.getString()
        paragraphs = shape.createEnumeration()
        runs = []
        paragraph_index = 0
        while paragraphs.hasMoreElements():
            portions = paragraphs.nextElement().createEnumeration()
            while portions.hasMoreElements():
                portion = portions.nextElement()
                runs.append({"paragraph": paragraph_index, "text": portion.getString(),
                             "font": portion.CharFontName, "points": portion.CharHeight})
            paragraph_index += 1
        result["textRuns"] = runs
    except Exception:
        pass
    for key in ["FillColor", "CharFontName", "TextAutoGrowHeight", "TextFitToSize"]:
        try:
            result[key] = str(shape.getPropertyValue(key))
        except Exception:
            pass
    if result["type"] == "com.sun.star.drawing.TableShape":
        model = shape.Model
        result["rows"] = [[model.getCellByPosition(c, r).getString()
                           for c in range(model.Columns.Count)] for r in range(model.Rows.Count)]
    if result["type"] == "com.sun.star.drawing.GraphicObjectShape":
        result["pixels"] = {"width": shape.Graphic.SizePixel.Width, "height": shape.Graphic.SizePixel.Height}
    return result


def capture(doc):
    pages = doc.getDrawPages()
    return [{"index": i, "width": pages.getByIndex(i).Width, "height": pages.getByIndex(i).Height,
             "shapes": [capture_shape(pages.getByIndex(i).getByIndex(j))
                        for j in range(pages.getByIndex(i).Count)]} for i in range(pages.Count)]


def export_visuals(doc, context, out, suffix):
    pdf = out / (suffix + ".pdf")
    doc.storeToURL(url(pdf), (prop("FilterName", "impress_pdf_Export"), prop("Overwrite", True)))
    assert pdf.read_bytes().startswith(b"%PDF-"), "PDF must exist"
    files = []
    for i in range(doc.getDrawPages().Count):
        exporter = context.ServiceManager.createInstanceWithContext("com.sun.star.drawing.GraphicExportFilter", context)
        exporter.setSourceDocument(doc.getDrawPages().getByIndex(i))
        image = out / (suffix + "-slide-" + str(i + 1) + ".png")
        assert exporter.filter((prop("MediaType", "image/png"), prop("URL", url(image)),
                                prop("FilterData", (prop("PixelWidth", 960), prop("PixelHeight", 540)))))
        assert image.read_bytes().startswith(b"\x89PNG\r\n\x1a\n"), "PNG must exist"
        files.append(image.name)
    return {"pdf": pdf.name, "slidePNGs": files}


def load(desktop, path):
    return desktop.loadComponentFromURL(url(path), "_blank", 0,
        (prop("Hidden", True), prop("ReadOnly", False), prop("MacroExecutionMode", 0), prop("UpdateDocMode", 0)))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--soffice", required=True)
    parser.add_argument("--input", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    out = Path(args.output).resolve()
    out.mkdir(parents=True, exist_ok=False)
    pipe = "slides-s0-" + uuid.uuid4().hex
    profile = out / "profile"
    env = dict(os.environ, SAL_USE_VCLPLUGIN="svp")
    with (out / "office-process.log").open("w") as logfile:
        process = subprocess.Popen([args.soffice, "-env:UserInstallation=" + url(profile), "--headless",
            "--nologo", "--nodefault", "--norestore", "--accept=pipe,name=" + pipe + ";urp;StarOffice.ComponentContext"],
            env=env, stdout=logfile, stderr=logfile)
        desktop = None
        try:
            local = uno.getComponentContext()
            resolver = local.ServiceManager.createInstanceWithContext("com.sun.star.bridge.UnoUrlResolver", local)
            deadline = time.monotonic() + 30
            restarts = 0
            while True:
                try:
                    context = resolver.resolve("uno:pipe,name=" + pipe + ";urp;StarOffice.ComponentContext")
                    break
                except Exception:
                    if process.poll() == 81 and restarts < 2:
                        restarts += 1
                        process = subprocess.Popen([args.soffice, "-env:UserInstallation=" + url(profile), "--headless", "--nologo", "--nodefault", "--norestore", "--accept=pipe,name=" + pipe + ";urp;StarOffice.ComponentContext"], env=env, stdout=logfile, stderr=logfile)
                    elif process.poll() is not None or time.monotonic() >= deadline:
                        raise RuntimeError("owned LibreOffice process did not become available; exit=" + str(process.poll()))
                    time.sleep(0.1)
            desktop = context.ServiceManager.createInstanceWithContext("com.sun.star.frame.Desktop", context)
            doc = load(desktop, args.input)
            assert doc and doc.supportsService("com.sun.star.presentation.PresentationDocument")
            before = capture(doc)
            (out / "before.json").write_text(json.dumps(before, ensure_ascii=False, indent=2) + "\n")
            assert len(before) == 5
            visuals = export_visuals(doc, context, out, "original")
            shapes = [doc.getDrawPages().getByIndex(i).getByIndex(j)
                      for i in range(doc.getDrawPages().Count) for j in range(doc.getDrawPages().getByIndex(i).Count)]
            title = next(s for s in shapes if s.Name == "title-czech")
            expected_text = title.getString() + "\nUNO: Upravený český text"
            title.insertString(title.getEnd(), "\nUNO: Upravený český text", False)
            rectangle = next(s for s in shapes if s.Name == "shape-rect")
            position = rectangle.Position
            position.X += 26
            rectangle.Position = position
            rectangle.FillColor = 0x955AC3
            table = next(s for s in shapes if s.getShapeType() == "com.sun.star.drawing.TableShape")
            table.Model.getCellByPosition(0, 1).setString("Plzeň – UNO")
            edited = out / "edited.pptx"
            doc.storeToURL(url(edited), (prop("FilterName", "Impress MS PowerPoint 2007 XML"), prop("Overwrite", True)))
            edited_visuals = export_visuals(doc, context, out, "edited")
            doc.close(True)
            reopened = load(desktop, edited)
            after = capture(reopened)
            flat = [s for p in after for s in p["shapes"]]
            assert next(s for s in flat if s["name"] == "title-czech")["text"] == expected_text
            shape_after = next(s for s in flat if s["name"] == "shape-rect")
            assert abs(shape_after["x"] - position.X) <= 1 and shape_after["FillColor"] == str(0x955AC3)
            assert next(s for s in flat if "rows" in s)["rows"][1][0] == "Plzeň – UNO"
            assert next(s for s in flat if s["name"] == "image-local")["pixels"] == {"width": 96, "height": 64}
            reopened.close(True)
            report = {"kind": "LibreOffice actual application UNO open/native-edit/save/reopen acceptance",
                      "input": Path(args.input).name, "before": before, "after": after,
                      "originalVisuals": visuals, "editedVisuals": edited_visuals,
                      "nativeEditsSavedReopened": ["Czech text with LF", "rectangle geometry/color", "table cell"],
                      "manualUIEditing": "not performed; native application objects edited through UNO",
                      "powerPoint": "not performed; LibreOffice is not PowerPoint"}
            (out / "uno-report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
            print(json.dumps({"output": str(out), "nativeEditsSavedReopened": report["nativeEditsSavedReopened"]}))
        finally:
            if desktop is not None:
                desktop.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


if __name__ == "__main__":
    main()
