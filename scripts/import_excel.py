"""Convert the supplied workbooks without third-party dependencies.
Run from the repository root. Never adds document sales to monthly totals.
"""
import collections, datetime as dt, gzip, json, math, pathlib, re, statistics, zipfile
import xml.etree.ElementTree as ET
NS = {'s': 'http://schemas.openxmlformats.org/spreadsheetml/2006/main'}
MONTHS = ['янв','фев','мар','апр','май','июн','июл','авг','сен','окт','ноя','дек']
report = {'files': [], 'issues': collections.Counter()}
def workbook(path):
    z = zipfile.ZipFile(path)
    strings = [''.join(x.itertext()).strip() for x in ET.fromstring(z.read('xl/sharedStrings.xml'))] if 'xl/sharedStrings.xml' in z.namelist() else []
    sheets = []
    names = [x.get('name') for x in ET.fromstring(z.read('xl/workbook.xml')).find('s:sheets', NS)]
    for name, f in zip(names, sorted((f for f in z.namelist() if re.fullmatch(r'xl/worksheets/sheet\d+.xml', f)))):
        rows = []
        with z.open(f) as stream:
            for _, elem in ET.iterparse(stream, events=('end',)):
                if elem.tag != '{'+NS['s']+'}row': continue
                row = {}
                for c in elem:
                    v = c.find('s:v', NS)
                    val = v.text if v is not None else ''.join(c.find('s:is', NS).itertext()) if c.find('s:is', NS) is not None else ''
                    if c.get('t') == 's' and val: val = strings[int(val)]
                    if val: row[re.sub(r'\d', '', c.get('r'))] = val.strip()
                if row: rows.append(row)
                elem.clear()
        sheets.append(rows)
        report['files'].append({'file':str(path),'sheet':name,'rows':len(rows),'headers':rows[:3]})
    return sheets
def num(s):
    if not s: return 0.
    if str(s).startswith('#'):
        report['issues']['excel_error_cells']+=1
        return 0.
    return float(str(s).replace('\xa0','').replace(' ','').replace(',','.'))
def month(s):
    match = re.search(r'(202[456])',s)
    if match:
        for i,m in enumerate(MONTHS):
            if s.lower().startswith(m): return f'{match[1]}-{i+1:02d}-01'
def code(s): return s.strip()
products, monthly, stocks, shipments, seasons = {}, {}, {}, [], []
def product(sup, c, name):
    c=code(c)
    if not c or c in ('Номенклатура.Код','Код 1с','Код') or not name or name=='Итого': return None
    key=sup+':'+c
    if key not in products:
        products[key]={'id':key,'sku':c,'name':name,'supplier_id':sup,'pack_size':1,'min_order_quantity':0,'notes':'','stock_date':'2026-09-01'}
    return products[key]
def note(p, s):
    if s not in p['notes']: p['notes'] += s+' '
files = {p.name: workbook(p) for p in sorted(pathlib.Path('.').rglob('*.xlsx'))}
for sup, salesfile, stockfile, moqfile, transitfile, seasonfile, dynfile in [
 ('iek','Ежемесячные продажи в количественном выражении за последние 2 года.xlsx','Ежемесячные остатки продукции за последние 2 года  ИЭК.xlsx','MOQ  ИЭК.xlsx','Путь ИЭК 22.09.2026.xlsx','Сезонность ИЭК.xlsx','Динамика продаж_2025-2026.xlsx'),
 ('se','Ежемесячные продажи в кол-м выражении SystemElectric 2024-2026.xlsx','Ежемесячные остатки SystemElectric 2024-2026.xlsx','MOQ SystemElectric.xlsx','Товар в пути_SystemElectric на 22.09.2026.xlsx','Сезонность SystemElectric 2024-2026.xlsx','Динамика продаж_Syseme Electric_2025-2026.xlsx')]:
    rows=files[salesfile][0]; cols={c:month(v) for c,v in rows[0].items() if month(v)}
    for r in rows[1:]:
        p=product(sup,r.get('B',''),r.get('A',''))
        if not p: continue
        if sup=='se' and r.get('C'): p['sku']=r['C']
        for c,m in cols.items():
            q=num(r.get(c))
            monthly[p['id'],m]={'product_id':p['id'],'date':m,'quantity':max(0,q),'stock':None,'spike_excess':0}
            if q<0: note(p,'Отрицательные месячные продажи (возвраты) ограничены нулём.')
    rows=files[stockfile][0]; cols={c:month(v) for c,v in rows[0].items() if month(v)}
    for r in rows[1:]:
        p=product(sup,r.get('C',''),r.get('A' if sup=='iek' else 'B',''))
        if not p: continue
        for c,m in cols.items():
            # Blank cells in these pivot exports denote no balance.
            q=max(0,num(r.get(c)))
            if (p['id'],m) in monthly: monthly[p['id'],m]['stock']=q
            if m==max(cols.values()): stocks[p['id']]={'product_id':p['id'],'on_hand':math.floor(q),'reserved':0}
        note(p,'Остаток: месячный срез 01.09.2026, не текущая инвентаризация; проверьте перед заказом.')
    rows=files[moqfile][0]
    for r in rows[1:]:
        p=product(sup,r.get('B' if sup=='iek' else 'C',''),r.get('D' if sup=='iek' else 'B',''))
        if not p: continue
        article=r.get('C' if sup=='iek' else 'D')
        if article:p['sku']=article
        q=math.ceil(num(r.get('E')))
        if q>0:
            if sup=='iek':
                p['min_order_quantity']=q
                note(p,'MOQ из «Мин. разр. к отгр.»; отдельная кратность неизвестна, принята 1.')
            else:p['pack_size']=q
        else:note(p,'MOQ/кратность отсутствует или равна нулю: принята 1.')
    rows=files[transitfile][0]
    header=next(r for r in rows if 'Код 1с' in r.values())
    for idx,r in enumerate(rows):
        p=product(sup,r.get('A' if sup=='iek' else 'C',''),r.get('C' if sup=='iek' else 'D',''))
        if not p: continue
        if r.get('B'):p['sku']=r['B']
        if sup=='se':
            on=max(0,num(r.get('AX'))); reserve=max(0,num(r.get('AY')))
            free=max(0,num(r.get('AZ'))) if 'AZ' in r else max(0,on-reserve)
            stocks[p['id']]={'product_id':p['id'],'on_hand':math.floor(on),'reserved':math.floor(on)-math.floor(min(on,free))}
            p['stock_date']='2026-09-22'
            p['notes']=p['notes'].replace('Остаток: месячный срез 01.09.2026, не текущая инвентаризация; проверьте перед заказом. ','')
            dates={'BC':'2026-09-24'}
        else:
            dates={}
            for c,h in header.items():
                match=re.search(r'поступление до (\d{2}\.\d{2}\.\d{4})',h)
                if match:dates[c]=dt.datetime.strptime(match[1],'%d.%m.%Y').strftime('%Y-%m-%d')
        for c,date in dates.items():
            q=math.floor(max(0,num(r.get(c))))
            if q:shipments.append({'id':f'{sup}-{idx}-{c}','product_id':p['id'],'quantity':q,'expected_date':date})
        if 'БУХТАМИ' in p['name']:
            note(p,'Проверьте перевод бухт в метры: единица поставки в Excel не определена однозначно.')
    for r in files[seasonfile][0]:
        if r.get('B') in MONTHS and r.get('L') and r.get('A') != 'год':
            seasons.append({'supplier_id':sup,'month':MONTHS.index(r['B'])+1,'factor':num(r['L'])})
    daily=collections.defaultdict(float)
    for r in files[dynfile][0][1:]:
        if not r.get('C','').startswith('Расходная'):continue
        try: date=dt.datetime.strptime(r['A'][:10],'%d.%m.%Y').strftime('%Y-%m-%d')
        except (ValueError,KeyError):continue
        key=sup+':'+code(r.get('D',''))
        if key not in products:
            report['issues']['unmatched_dynamic_rows']+=1;continue
        daily[key,date]+=num(r.get('H'))
    byproduct=collections.defaultdict(list)
    for (key,date),q in daily.items():
        if date>='2024-01-01' and q>0:byproduct[key].append(q)
    limits={}
    for key,values in byproduct.items():
        if len(values)>=4:
            med=statistics.median(values); mad=statistics.median(abs(v-med) for v in values)
            limits[key]=(med,max(3*med,med+3*1.4826*mad))
    daily_month_totals=collections.defaultdict(float)
    for (key,date),q in daily.items():
        daily_month_totals[key,date[:7]+'-01']+=max(0,q)
    for (key,date),q in daily.items():
        m=(key,date[:7]+'-01')
        if key in limits and m in monthly and q>limits[key][1]:
            monthly[m]['spike_excess']+=monthly[m]['quantity']*(q-limits[key][0])/daily_month_totals[m]
            report['issues']['daily_spikes']+=1
history_ids={k[0] for k in monthly}
for p in products.values():
    if p['id'] not in stocks:
        stocks[p['id']]={'product_id':p['id'],'on_hand':0,'reserved':0}
        note(p,'Остаток отсутствует: для предварительного расчёта принят 0, требуется проверка.')
    if p['id'] not in history_ids:note(p,'Месячная история продаж отсутствует; автоматический спрос не оценён.')
    if p['pack_size']==1 and p['min_order_quantity']==0:note(p,'MOQ/кратность не подтверждены: расчёт по 1 единице.')
    note(p,'Срок новой поставки отсутствует в источниках: принято 14 дней.')
# Supplier article is not a unique product key; keep duplicates but warn.
articles=collections.Counter((p['supplier_id'],p['sku']) for p in products.values())
for p in products.values():
    if articles[p['supplier_id'],p['sku']]>1:note(p,'Артикул встречается у нескольких кодов 1С; строки не объединены.')
data={'suppliers':[{'id':'iek','name':'IEK','lead_time_days':14},{'id':'se','name':'System Electric','lead_time_days':14}],
      'products':list(products.values()),'sales':[],'stock':list(stocks.values()),'shipments':shipments,
      'monthly':list(monthly.values()),'seasonality':seasons,'source_date':'2026-09-22'}
out=pathlib.Path('internal/realdata/dataset.json.gz')
with out.open('wb') as f:
    with gzip.GzipFile(fileobj=f,mode='wb',mtime=0) as g:g.write(json.dumps(data,ensure_ascii=False,separators=(',',':')).encode())
report['counts']={k:len(v) for k,v in data.items() if isinstance(v,list)}
pathlib.Path('internal/realdata/import-report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2))
print(json.dumps(report['counts']), dict(report['issues']),flush=True)
