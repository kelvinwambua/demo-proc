import re
import sys
from typing import Any, Dict, List, Optional

import spacy
from fastapi import FastAPI
from pydantic import BaseModel
from spacy.lang.en import English
from spacy.pipeline import EntityRuler

KENYAN_PLACES = [
    "Nairobi",
    "Kisumu",
    "Mombasa",
    "Nakuru",
    "Eldoret",
    "Kitale",
    "Kakamega",
    "Bungoma",
    "Busia",
    "Malaba",
    "Kampala",
    "Kigali",
    "Juba",
    "Dar es Salaam",
    "Arusha",
    "Dodoma",
    "Tanga",
    "Mwanza",
    "Kajiado",
    "Kibwezi",
    "Emali",
    "Athi River",
    "Mai Mahiu",
    "Naivasha",
    "Gilgil",
    "Kericho",
    "Kisii",
    "Migori",
    "Homa Bay",
    "Siaya",
    "Bondo",
    "Webuye",
    "Lodwar",
    "Marsabit",
    "Isiolo",
    "Garissa",
    "Wajir",
    "Mandera",
    "Lamu",
    "Malindi",
    "Kilifi",
    "Mtwapa",
    "Kwale",
    "Voi",
    "Taveta",
    "Machakos",
    "Kitui",
    "Embu",
    "Meru",
    "Nyeri",
    "Karatina",
    "Muranga",
    "Kiambu",
    "Thika",
    "Ruiru",
    "Juja",
    "Nairobi West",
    "Industrial Area",
    "Embakasi",
    "Mombasa Road",
    "Uhuru Highway",
    "Waiyaki Way",
    "Thika Road",
    "Juja Road",
    "Kangemi",
    "Kawangware",
    "Kibera",
    "Dagoretti",
    "Karen",
    "Westlands",
    "Parklands",
    "Kilimani",
    "Kileleshwa",
    "Lavington",
    "Riverside",
    "Upper Hill",
    "CBD",
    "Donholm",
    "Buruburu",
    "Umoja",
    "Komarock",
    "Kayole",
    "Nyayo Highrise",
    "Madaraka",
    "Langata",
    "Rongai",
    "Kitengela",
    "Syokimau",
    "Imara Daima",
    "South B",
    "South C",
    "Eastleigh",
    "Kasarani",
    "Roysambu",
    "Kahawa West",
    "Githurai",
    "Ruai",
    "Mwiki",
    "Mombasa CBD",
    "Nyali",
    "Bamburi",
    "Likoni",
    "Kisauni",
    "Changamwe",
    "Mikindani",
    "Jomvu",
]

URGENCY_KEYWORDS = [
    "urgent",
    "immediate",
    "rush",
    "express",
    "emergency",
    "priority",
    "asap",
    "same day",
    "next day",
    "24h",
    "48h",
    "critical",
    "time-sensitive",
]

CARGO_TYPES = [
    "cement",
    "sand",
    "gravel",
    "ballast",
    "steel",
    "iron",
    "timber",
    "lumber",
    "maize",
    "corn",
    "beans",
    "wheat",
    "rice",
    "coffee",
    "tea",
    "sugar",
    "salt",
    "fertilizer",
    "pesticides",
    "fuel",
    "diesel",
    "petrol",
    "kerosene",
    "cooking oil",
    "wheat flour",
    "maize flour",
    "construction materials",
    "general cargo",
    "fragile goods",
    "hazardous goods",
    "chemicals",
    "pharmaceuticals",
    "medical supplies",
    "perishables",
    "vegetables",
    "fruits",
    "meat",
    "dairy",
    "fish",
    "furniture",
    "household goods",
    "office equipment",
    "electronics",
    "machinery",
    "heavy equipment",
    "excavators",
    "bulldozers",
    "cranes",
    "trucks",
    "trailers",
    "containers",
    "vehicles",
    "cars",
    "buses",
    "motorcycles",
]

WEIGHT_VOLUME_REGEX = re.compile(
    r"\d+(\.\d+)?\s*(tons?|tonnes?|kg|kgs|kilos?|kilograms?|g|grams?|m3|cubic meter|cubic meters|cbm|cu\.?m\.?|liters?|ltrs?|l)",
    re.IGNORECASE,
)

CUSTOMER_NAME_REGEX = re.compile(
    r"(?:From|Customer|Company|Organization|Client):\s*([A-Z][A-Za-z0-9&\s.,'-]{2,50})",
    re.IGNORECASE,
)

try:
    nlp = spacy.load("en_core_web_sm")
except Exception as e:
    print(f"ERROR: Failed to load spaCy model 'en_core_web_sm': {e}")
    print("Please install it with: python -m spacy download en_core_web_sm")
    sys.exit(1)

patterns = []
for place in KENYAN_PLACES:
    patterns.append({"label": "KENYAN_PLACE", "pattern": [{"LOWER": place.lower()}]})

for keyword in URGENCY_KEYWORDS:
    tokens = keyword.split()
    pattern_tokens = [{"LOWER": t.lower()} for t in tokens]
    patterns.append({"label": "URGENCY", "pattern": pattern_tokens})

for cargo in CARGO_TYPES:
    tokens = cargo.split()
    pattern_tokens = [{"LOWER": t.lower()} for t in tokens]
    patterns.append({"label": "CARGO_TYPE", "pattern": pattern_tokens})

ruler = nlp.add_pipe("entity_ruler", before="ner")
ruler.add_patterns(patterns)


class ExtractRequest(BaseModel):
    text: str


class ExtractResponse(BaseModel):
    customer_name: Optional[str] = None
    cargo_from: Optional[str] = None
    destination: Optional[str] = None
    mobilization_point: Optional[str] = None
    cargo_type: Optional[str] = None
    weight_volume: Optional[str] = None
    delivery_urgency: Optional[str] = None
    special_handling: Optional[str] = None
    confidence: Dict[str, float]
    unresolved: List[str]


app = FastAPI()


@app.get("/health")
def health() -> Dict[str, str]:
    return {"status": "ok", "model": "en_core_web_sm"}


@app.post("/extract", response_model=ExtractResponse)
def extract(req: ExtractRequest) -> ExtractResponse:
    text = req.text
    doc = nlp(text)

    kenyan_places: List[str] = []
    gpe_loc: List[str] = []
    urgency_matches: List[str] = []
    cargo_matches: List[str] = []

    confidence: Dict[str, float] = {}

    for ent in doc.ents:
        if ent.label_ == "KENYAN_PLACE":
            normalized = ent.text
            if normalized not in kenyan_places:
                kenyan_places.append(normalized)
        elif ent.label_ == "URGENCY":
            if ent.text.lower() not in [u.lower() for u in urgency_matches]:
                urgency_matches.append(ent.text)
        elif ent.label_ == "CARGO_TYPE":
            if ent.text.lower() not in [c.lower() for c in cargo_matches]:
                cargo_matches.append(ent.text)
        elif ent.label_ in ("GPE", "LOC"):
            if ent.text not in gpe_loc and ent.text not in kenyan_places:
                gpe_loc.append(ent.text)

    all_places = kenyan_places + gpe_loc
    cargo_from = all_places[0] if len(all_places) > 0 else None
    destination = all_places[1] if len(all_places) > 1 else None

    if cargo_from is not None:
        confidence["cargo_from"] = 0.9 if cargo_from in kenyan_places else 0.6
    if destination is not None:
        confidence["destination"] = 0.9 if destination in kenyan_places else 0.6

    wv_matches = []
    for m in WEIGHT_VOLUME_REGEX.finditer(text):
        wv_matches.append(m.group(0))
    weight_volume = ", ".join(wv_matches) if wv_matches else None
    if weight_volume is not None:
        confidence["weight_volume"] = 0.9

    customer_name = None
    cm = CUSTOMER_NAME_REGEX.search(text)
    if cm:
        customer_name = cm.group(1).strip().rstrip(".,")
        confidence["customer_name"] = 0.9

    cargo_type = ", ".join(cargo_matches) if cargo_matches else None
    if cargo_type is not None:
        confidence["cargo_type"] = 0.9

    delivery_urgency = ", ".join(urgency_matches) if urgency_matches else None
    if delivery_urgency is not None:
        confidence["delivery_urgency"] = 0.9

    # Mobilization point — regex on common OML field labels
    mobilization_point = None
    mob_re = re.search(
        r"(?:mobiliz(?:ation|isation)|mobiliz(?:e|ing)|depot|yard|truck park)[\s\-:]+([A-Z][A-Za-z\s\-]{2,40})",
        text,
        re.IGNORECASE,
    )
    if mob_re:
        mobilization_point = mob_re.group(1).strip().rstrip(".,")
        confidence["mobilization_point"] = 0.85

    # Special handling — regex on common OML field labels
    special_handling = None
    sh_re = re.search(
        r"(?:special\s+handling|special\s+requirements|special\s+instructions)[:\s]+(.+?)(?:\n\n|\.$|\Z)",
        text,
        re.IGNORECASE | re.DOTALL,
    )
    if sh_re:
        sh_text = sh_re.group(1).strip()
        if sh_text:
            special_handling = sh_text[:300]
            confidence["special_handling"] = 0.80

    unresolved: List[str] = []
    for field in [
        "customer_name",
        "cargo_from",
        "destination",
        "mobilization_point",
        "cargo_type",
        "weight_volume",
        "delivery_urgency",
        "special_handling",
    ]:
        val = locals().get(field)
        if val is None:
            unresolved.append(field)

    return ExtractResponse(
        customer_name=customer_name,
        cargo_from=cargo_from,
        destination=destination,
        mobilization_point=mobilization_point,
        cargo_type=cargo_type,
        weight_volume=weight_volume,
        delivery_urgency=delivery_urgency,
        special_handling=special_handling,
        confidence=confidence,
        unresolved=unresolved,
    )
