`openpyxl-hyperlink-chartsheet.xlsx` was generated with openpyxl 3.1.5:

```python
from openpyxl import Workbook
from openpyxl.chart import BarChart, Reference
w = Workbook()
s = w.active
s.title = 'Data'
s.append(['Žofie', 42])
s['A1'].hyperlink = 'https://untrusted.test/private'
c = BarChart()
c.add_data(Reference(s, min_col=2, min_row=1, max_row=1))
w.create_chartsheet('Chart').add_chart(c)
w.create_sheet('After chart').append(['retained'])
w.save('openpyxl-hyperlink-chartsheet.xlsx')
```

The hyperlink is synthetic and must never be fetched. Tests need only the saved
fixture; openpyxl is not a build dependency.
